package tasks

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Serve and the CLI are two processes on one file: two Repos with their own
// connections are the same thing to SQLite.
func TestServeAndCLIEditTheSameDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	serve, cli := openRepo(t, path), openRepo(t, path)

	done := mustCreate(t, serve, CreateInput{Title: "to finish", Place: PlaceBacklog, ProjectKey: "p"})

	var wg sync.WaitGroup
	var reqErr, doneErr error
	var req AgentTask
	wg.Add(2)
	go func() {
		defer wg.Done()
		req, reqErr = cli.AgentAsk(bg, actor("cli-session", "p"), AgentInput{Title: "put the secret in GitHub"})
	}()
	go func() {
		defer wg.Done()
		_, doneErr = serve.Update(bg, done.ID, done.Revision, Patch{Status: ptr(StatusDone)})
	}()
	wg.Wait()
	if reqErr != nil || doneErr != nil {
		t.Fatalf("ask: %v, done: %v", reqErr, doneErr)
	}
	if req.ID == done.ID {
		t.Fatalf("both tasks got id %d", req.ID)
	}

	// Reload from a third connection: both are stored.
	fresh := openRepo(t, path)
	res, err := fresh.List(bg, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tasks) != 2 {
		t.Fatalf("want 2 tasks after reload, got %+v", res.Tasks)
	}
	if res.Counts.OpenRequests != 1 {
		t.Fatalf("open requests = %d, want 1", res.Counts.OpenRequests)
	}
}

func TestConcurrentWritersFromTwoConnectionsLoseNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	repos := []*Repo{openRepo(t, path), openRepo(t, path)}
	const perRepo = 40
	var wg sync.WaitGroup
	var failures atomic.Int32
	for i, r := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < perRepo; n++ {
				if _, err := r.AgentCreate(bg, actor([]string{"a", "b"}[i], "p"), AgentInput{Title: "t"}); err != nil {
					failures.Add(1)
					t.Errorf("repo %d: %v", i, err)
				}
			}
		}()
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.FailNow()
	}
	res, err := repos[0].List(bg, Filter{IncludeAgents: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tasks) != 2*perRepo {
		t.Fatalf("stored %d, want %d", len(res.Tasks), 2*perRepo)
	}
	seen := map[int64]bool{}
	for _, tk := range res.Tasks {
		if seen[tk.ID] {
			t.Fatalf("duplicate id %d", tk.ID)
		}
		seen[tk.ID] = true
	}
	rev, _ := repos[1].Revision(bg)
	if rev != 2*perRepo {
		t.Fatalf("revision %d, want %d (one bump per committed change)", rev, 2*perRepo)
	}
}

func TestWatchSeesAnotherProcessWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	serve, cli := openRepo(t, path), openRepo(t, path)
	// The database exists before the watcher starts, as it does in a running serve.
	mustCreate(t, serve, CreateInput{Title: "seed", Place: PlaceYou})

	ctx, cancel := contextWithCancel(t)
	defer cancel()
	var last atomic.Int64
	go serve.Watch(ctx, 20*time.Millisecond, func(rev int64) { last.Store(rev) })
	time.Sleep(100 * time.Millisecond) // baseline

	if _, err := cli.AgentAsk(bg, actor("cli", "p"), AgentInput{Title: "from the cli"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "serve to notice the CLI commit", func() bool { return last.Load() == 2 })

	// And its own commits: a different connection from the watcher's reader.
	mustCreate(t, serve, CreateInput{Title: "local", Place: PlaceYou})
	waitFor(t, "watcher to notice a local commit", func() bool { return last.Load() == 3 })
}

func TestWatchWaitsForTheDatabaseToAppear(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	serve, cli := openRepo(t, path), openRepo(t, path)
	ctx, cancel := contextWithCancel(t)
	defer cancel()
	var got atomic.Int64
	go serve.Watch(ctx, 20*time.Millisecond, func(rev int64) { got.Store(rev) })
	time.Sleep(60 * time.Millisecond)
	mustCreate(t, cli, CreateInput{Title: "first ever", Place: PlaceYou})
	waitFor(t, "first commit after creation", func() bool { return got.Load() == 1 })
}

func TestReadingAMissingDatabaseCreatesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", DatabaseName)
	r := openRepo(t, path)
	res, err := r.List(bg, Filter{IncludeAgents: true})
	if err != nil || len(res.Tasks) != 0 {
		t.Fatalf("list: %+v, %v", res, err)
	}
	view, err := r.AgentList(bg, actor("a", "p"))
	if err != nil || len(view.Checklist) != 0 {
		t.Fatalf("agent list: %+v, %v", view, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a read created the database: %v", err)
	}
}

func TestDatabaseIsPrivateAndVersioned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", DatabaseName)
	r := openRepo(t, path)
	mustCreate(t, r, CreateInput{Title: "x", Place: PlaceYou})
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("db mode: %v %v", fi, err)
	}
	di, _ := os.Stat(filepath.Dir(path))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode: %v", di.Mode().Perm())
	}
	db := rawDB(t, path)
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("user_version = %d, %v", v, err)
	}
	var mode string
	_ = db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if mode != "wal" {
		t.Fatalf("journal_mode = %q", mode)
	}
	// Reopening applies nothing twice.
	r2 := openRepo(t, path)
	if res, err := r2.List(bg, Filter{}); err != nil || len(res.Tasks) != 1 {
		t.Fatalf("reopen: %+v %v", res, err)
	}
}

func TestNewerDatabaseIsReadableButNeverWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	r := openRepo(t, path)
	mustCreate(t, r, CreateInput{Title: "kept", Place: PlaceYou})
	_ = r.Close()
	db := rawDB(t, path)
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}

	old := openRepo(t, path)
	res, err := old.List(bg, Filter{})
	if err != nil || len(res.Tasks) != 1 {
		t.Fatalf("read of newer db: %+v %v", res, err)
	}
	if _, err := old.Create(bg, CreateInput{Title: "no", Place: PlaceYou}); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("write on newer db: %v", err)
	}
	var v, n int
	_ = db.QueryRow("PRAGMA user_version").Scan(&v)
	_ = db.QueryRow("SELECT count(*) FROM tasks").Scan(&n)
	if v != 99 || n != 1 {
		t.Fatalf("database was touched: version %d, %d tasks", v, n)
	}
}

func TestStaleRevisionGetsAConflictWithTheCurrentTask(t *testing.T) {
	r := newRepo(t)
	cli := openRepo(t, r.Path())
	rec := mustCreate(t, r, CreateInput{Title: "old title", Place: PlaceYou})

	// The CLI edits it after the form was loaded.
	if _, err := cli.Update(bg, rec.ID, rec.Revision, Patch{Title: ptr("cli title")}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Update(bg, rec.ID, rec.Revision, Patch{Title: ptr("stale form")})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	if conflict.Current.Title != "cli title" || conflict.Current.Revision != rec.Revision+1 {
		t.Fatalf("conflict carries %+v", conflict.Current)
	}
	got, _ := r.Get(bg, rec.ID)
	if got.Title != "cli title" {
		t.Fatalf("stale form overwrote: %q", got.Title)
	}
	if err := r.Delete(bg, rec.ID, rec.Revision); !errors.As(err, &conflict) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := r.Delete(bg, rec.ID, got.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(bg, rec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestPlaceRules(t *testing.T) {
	r := newRepo(t)
	if _, err := r.Create(bg, CreateInput{Title: "no project", Place: PlaceBacklog}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("backlog without project: %v", err)
	}
	if _, err := r.Create(bg, CreateInput{Title: "x", Place: PlaceYou, AssigneeSessionID: "s"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("you with assignee: %v", err)
	}
	if _, err := r.Create(bg, CreateInput{Title: "  ", Place: PlaceYou}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank title: %v", err)
	}

	// A request stays a request through ordinary edits; only an explicit move
	// takes the requester off it.
	req, err := r.AgentAsk(bg, actor("A", "p"), AgentInput{Title: "need a key"})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := r.Get(bg, req.ID)
	rec, err = r.Update(bg, rec.ID, rec.Revision, Patch{Description: ptr("more detail"), Title: ptr("need the key")})
	if err != nil || rec.RequesterSessionID != "A" {
		t.Fatalf("ordinary edit: %+v %v", rec, err)
	}
	rec, err = r.Update(bg, rec.ID, rec.Revision, Patch{Place: ptrPlace(PlaceBacklog)})
	if err != nil || rec.RequesterSessionID != "" || rec.Place != PlaceBacklog || rec.ProjectKey != "p" {
		t.Fatalf("move to backlog: %+v %v", rec, err)
	}
	// Assigning needs a session.
	if _, err := r.Update(bg, rec.ID, rec.Revision, Patch{Place: ptrPlace(PlaceAgent)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("agent without session: %v", err)
	}
	rec, err = r.Update(bg, rec.ID, rec.Revision, Patch{Place: ptrPlace(PlaceAgent), AssigneeSessionID: ptr("B")})
	if err != nil || rec.AssigneeSessionID != "B" {
		t.Fatalf("assign: %+v %v", rec, err)
	}
}

func TestDoneStaysInDoneThenArchivesAfterSevenDays(t *testing.T) {
	r := newRepo(t)
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	clock := base
	r.SetClock(func() time.Time { return clock })

	stale := mustCreate(t, r, CreateInput{Title: "old but open", Place: PlaceYou})
	fin := mustCreate(t, r, CreateInput{Title: "finished", Place: PlaceYou})
	clock = base.Add(30 * 24 * time.Hour) // creation age alone never archives
	fin, err := r.Update(bg, fin.ID, fin.Revision, Patch{Status: ptr(StatusDone)})
	if err != nil || fin.CompletedAt == 0 {
		t.Fatalf("complete: %+v %v", fin, err)
	}

	clock = clock.Add(7*24*time.Hour - time.Minute)
	res, _ := r.List(bg, Filter{})
	if len(res.Tasks) != 2 || findTask(res.Tasks, fin.ID).ArchivedAt != 0 {
		t.Fatalf("before seven days: %+v", res.Tasks)
	}
	if res.Counts.You != 1 {
		t.Fatalf("done must not count as open: %+v", res.Counts)
	}

	clock = clock.Add(2 * time.Minute)
	res, _ = r.List(bg, Filter{})
	if len(res.Tasks) != 1 || res.Tasks[0].ID != stale.ID {
		t.Fatalf("after seven days the done task must be archived: %+v", res.Tasks)
	}
	all, _ := r.List(bg, Filter{IncludeArchived: true})
	if got := findTask(all.Tasks, fin.ID); got.ArchivedAt == 0 || got.Status != StatusDone {
		t.Fatalf("archived query: %+v", got)
	}
	// Reading again archives nothing new and changes no revision.
	before, _ := r.Revision(bg)
	_, _ = r.List(bg, Filter{})
	if after, _ := r.Revision(bg); after != before {
		t.Fatalf("idempotent archive bumped the revision: %d -> %d", before, after)
	}

	// Reopening an archived task clears both dates.
	arch, _ := r.Get(bg, fin.ID)
	re, err := r.Update(bg, arch.ID, arch.Revision, Patch{Status: ptr(StatusPending)})
	if err != nil || re.ArchivedAt != 0 || re.CompletedAt != 0 {
		t.Fatalf("reopen: %+v %v", re, err)
	}
}

func TestSubtasksAreOneLevelAndDependenciesCannotCycle(t *testing.T) {
	r := newRepo(t)
	you := note(t, r, "owner note")
	bl := backlog(t, r, "backlog task", "p")
	ag := mustCreate(t, r, CreateInput{Title: "agent task", Place: PlaceAgent, AssigneeSessionID: "A", ProjectKey: "p"})

	bl, err := r.Update(bg, bl.ID, bl.Revision, Patch{Subtasks: &[]SubtaskInput{{Title: "step 1"}, {Title: "step 2", Done: true}}})
	if err != nil || len(bl.Subtasks) != 2 || !bl.Subtasks[1].Done {
		t.Fatalf("subtasks: %+v %v", bl, err)
	}
	if _, err := r.Update(bg, bl.ID, bl.Revision, Patch{Subtasks: &[]SubtaskInput{{Title: " "}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank subtask: %v", err)
	}

	// Agents wait for the owner's note, across places.
	ag, err = r.Update(bg, ag.ID, ag.Revision, Patch{WaitsFor: &[]int64{you.ID}})
	if err != nil {
		t.Fatal(err)
	}
	youNow, _ := r.Get(bg, you.ID)
	if len(ag.WaitsFor) != 1 || ag.WaitsFor[0] != you.ID || len(youNow.Unblocks) != 1 || youNow.Unblocks[0] != ag.ID {
		t.Fatalf("waits/unblocks: %+v / %+v", ag, youNow)
	}

	// note waits for backlog waits for agent waits for note: a cycle.
	bl, _ = r.Get(bg, bl.ID)
	if _, err := r.Update(bg, bl.ID, bl.Revision, Patch{WaitsFor: &[]int64{ag.ID}}); err != nil {
		t.Fatal(err)
	}
	youNow, _ = r.Get(bg, you.ID)
	_, err = r.Update(bg, you.ID, youNow.Revision, Patch{WaitsFor: &[]int64{bl.ID}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle accepted: %v", err)
	}
	after, _ := r.Get(bg, you.ID)
	if len(after.WaitsFor) != 0 || after.Revision != youNow.Revision {
		t.Fatalf("a rejected cycle left traces: %+v", after)
	}
	if _, err := r.Update(bg, you.ID, after.Revision, Patch{WaitsFor: &[]int64{you.ID}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("self wait: %v", err)
	}
	if _, err := r.Update(bg, you.ID, after.Revision, Patch{WaitsFor: &[]int64{9999}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wait for a missing task: %v", err)
	}

	// Deleting a task removes its edges and its subtasks.
	cur, _ := r.Get(bg, ag.ID)
	if err := r.Delete(bg, cur.ID, cur.Revision); err != nil {
		t.Fatal(err)
	}
	youNow, _ = r.Get(bg, you.ID)
	bl, _ = r.Get(bg, bl.ID)
	if len(youNow.Unblocks) != 0 || len(bl.WaitsFor) != 0 {
		t.Fatalf("dangling edges: %+v %+v", youNow, bl)
	}
	db := rawDB(t, r.Path())
	var n int
	_ = db.QueryRow("SELECT count(*) FROM task_dependencies").Scan(&n)
	if n != 0 {
		t.Fatalf("%d dependency rows survive", n)
	}
}

func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func findTask(list []Record, id int64) Record {
	for _, r := range list {
		if r.ID == id {
			return r
		}
	}
	return Record{}
}

func ptr[T any](v T) *T       { return &v }
func ptrPlace(p Place) *Place { return &p }
