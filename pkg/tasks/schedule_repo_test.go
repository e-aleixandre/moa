package tasks

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a race-safe clock tests advance explicitly.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(at string) *fakeClock { return &fakeClock{t: utc(at)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(at time.Time) {
	c.mu.Lock()
	c.t = at
	c.mu.Unlock()
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func schedRepo(t *testing.T, at string) (*Repo, *fakeClock) {
	t.Helper()
	r := newRepo(t)
	c := newClock(at)
	r.SetClock(c.Now)
	return r, c
}

func sessionTarget(id string) Target { return Target{Kind: TargetSession, ID: id} }

func onceDef(at time.Time, late string) *ScheduleDef {
	return &ScheduleDef{When: When{Kind: WhenOnce, At: at.UnixMilli()}, TZ: "UTC",
		Target: sessionTarget("s1"), Delivery: Delivery{Late: late}}
}

func dailyDef(h, mi int, tz, late string) *ScheduleDef {
	r := daily(h, mi)
	return &ScheduleDef{When: When{Kind: WhenRepeat, Rule: &r}, TZ: tz,
		Target: sessionTarget("s1"), Delivery: Delivery{Late: late}}
}

func mkSchedule(t *testing.T, r *Repo, title string, def *ScheduleDef) Record {
	t.Helper()
	return mustCreate(t, r, CreateInput{Title: title, Description: "do " + title, Schedule: def,
		Subtasks: []SubtaskInput{{Title: "step one"}}})
}

func countRows(t *testing.T, r *Repo, table, where string, args ...any) int {
	t.Helper()
	db := rawDB(t, r.Path())
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE "+where, args...).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// applyV2 builds a database exactly as moa with schema version 2 left it.
func applyV2(t *testing.T, path string) *sql.DB {
	t.Helper()
	r := New(path)
	if _, _, _, err := r.ensure(false); err != nil { // no file: nothing opened
		t.Fatal(err)
	}
	_ = r.Close()
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for i, m := range migrations[:2] {
		if _, err := db.Exec(m); err != nil {
			t.Fatalf("migration %d: %v", i+1, err)
		}
	}
	if _, err := db.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestScheduleMigrationFromV2(t *testing.T) {
	path := t.TempDir() + "/tasks.sqlite"
	db := applyV2(t, path)
	stmts := []string{
		`INSERT INTO tasks(id,title,description,status,place,project_key,assignee_session_id,created_at,updated_at,revision)
			VALUES (1,'agent work','desc','in_progress','agent','p','s1',10,20,4)`,
		`INSERT INTO tasks(id,title,status,place,requester_session_id,created_at,updated_at) VALUES (2,'request','pending','you','s1',11,11)`,
		`INSERT INTO task_subtasks(task_id,title,done,position) VALUES (1,'a',1,0),(1,'b',0,1)`,
		`INSERT INTO task_dependencies(task_id,waits_for_id) VALUES (1,2)`,
		`INSERT INTO task_notifications(id,task_id,kind,recipient_session_id,deliver,method,title,body,state,created_at,updated_at)
			VALUES ('n1',1,'assigned','s1','hold','run','t','b','pending',1,1),
			       ('n2',1,'updated','s1','wake','run','t','b','held',2,2),
			       ('n3',1,'updated','s1','wake','append','t','b','sent',3,3)`,
		`UPDATE tasks_meta SET revision = 7`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	r := openRepo(t, path)
	if _, err := r.Create(bg, CreateInput{Title: "after upgrade", Place: PlaceYou}); err != nil {
		t.Fatalf("write after upgrade: %v", err)
	}
	raw := rawDB(t, path)
	var v int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("user_version = %d, %v", v, err)
	}
	var imported int
	if err := raw.QueryRow("SELECT legacy_schedules_imported FROM tasks_meta").Scan(&imported); err != nil || imported != 0 {
		t.Fatalf("import flag = %d, %v", imported, err)
	}
	for _, table := range []string{"task_schedules", "task_occurrences"} {
		var n int
		if err := raw.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s: n=%d err=%v", table, n, err)
		}
	}
	got := mustGet(t, r, 1)
	if got.Title != "agent work" || got.Status != StatusInProgress || got.Revision != 4 || got.UpdatedAt != 20 ||
		len(got.Subtasks) != 2 || !got.Subtasks[0].Done || len(got.WaitsFor) != 1 || got.WaitsFor[0] != 2 ||
		got.When != nil || got.OccurrenceID != 0 {
		t.Fatalf("old task changed: %+v", got)
	}
	open, err := r.OpenNotices(bg)
	if err != nil || len(open) != 3 {
		t.Fatalf("open notices = %d, %v", len(open), err)
	}
	if open[0].State != NoticePending || open[1].State != NoticeHeld || open[2].State != NoticeSent || open[2].Method != MethodAppend {
		t.Fatalf("notices changed: %+v", open)
	}
	if rev, _ := r.Revision(bg); rev != 8 {
		t.Fatalf("revision = %d, want 8 (7 + the new write)", rev)
	}
}

func TestScheduleConstraints(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-30T12:00:00Z")
	tmpl := mkSchedule(t, r, "valid", onceDef(clock.Now().Add(time.Hour), ""))
	child := mustCreate(t, r, CreateInput{Title: "child", Place: PlaceAgent, AssigneeSessionID: "s1"})
	db := rawDB(t, r.Path())
	ins := func(due int64, state string, child any, notice any) error {
		_, err := db.Exec(`INSERT INTO task_occurrences(schedule_task_id,due_at,definition_revision,spec_json,trigger,
			observed_at,state,resolved_session_id,child_task_id,notice_id,created_at,updated_at)
			VALUES (?,?,1,'{}','timer',1,?,'s1',?,?,1,1)`, tmpl.ID, due, state, child, notice)
		return err
	}
	if err := ins(100, OccAssigned, child.ID, "tn_a"); err != nil {
		t.Fatalf("valid occurrence refused: %v", err)
	}
	for name, err := range map[string]error{
		"duplicate slot":      ins(100, OccReady, nil, nil),
		"shared child":        ins(101, OccAssigned, child.ID, "tn_b"),
		"shared notice":       ins(102, OccAssigned, 999, "tn_a"),
		"canceled state":      ins(103, "canceled", nil, nil),
		"assigned w/o notice": ins(104, OccAssigned, nil, nil),
		"late with child":     ins(105, OccLate, 998, nil),
		"bad schedule kind": func() error {
			_, err := db.Exec(`INSERT INTO task_schedules(task_id,when_json,tz,target_json) VALUES (?,'{"kind":"cron"}','UTC','{"kind":"session","id":"s"}')`, child.ID)
			return err
		}(),
		"bad target kind": func() error {
			_, err := db.Exec(`INSERT INTO task_schedules(task_id,when_json,tz,target_json) VALUES (?,'{"kind":"once","at":5}','UTC','{"id":"s"}')`, child.ID)
			return err
		}(),
		"bad late policy": func() error {
			_, err := db.Exec(`INSERT INTO task_schedules(task_id,when_json,tz,target_json,late) VALUES (?,'{"kind":"once","at":5}','UTC','{"kind":"session","id":"s"}','later')`, child.ID)
			return err
		}(),
	} {
		if err == nil || !strings.Contains(err.Error(), "constraint") {
			t.Errorf("%s: accepted or wrong error: %v", name, err)
		}
	}

	for _, raw := range []string{
		`{"kind":"once","at":1790000000000.5}`,
		`{"kind":"repeat","rule":{"freq":"daily","h":9,"mi":0,"every":2}}`,
		`{"kind":"repeat","rule":{"freq":"daily","h":9}}`,
		`{"kind":"repeat","rule":{"freq":"monthly","dom":0,"h":9,"mi":0}}`,
		`{"kind":"repeat","rule":{"freq":"monthly","dom":32,"h":9,"mi":0}}`,
		`{"kind":"repeat","rule":{"freq":"daily","dow":1,"h":9,"mi":0}}`,
		`{"kind":"repeat","rule":{"freq":"daily","h":24,"mi":0}}`,
		`{"kind":"once","at":99999999999999999999}`,
		`{"kind":"once"}`,
	} {
		if _, err := DecodeWhen([]byte(raw)); !errors.Is(err, ErrInvalid) {
			t.Errorf("DecodeWhen(%s) = %v, want invalid", raw, err)
		}
	}
	if _, err := DecodeTarget([]byte(`{"kind":"session","id":"s","model":"x"}`)); !errors.Is(err, ErrInvalid) {
		t.Errorf("session target with model accepted: %v", err)
	}

	before, _ := r.Revision(bg)
	tasksBefore := countRows(t, r, "tasks", "1=1")
	dom0, dom32 := 0, 32
	future := clock.Now().Add(time.Hour)
	for name, def := range map[string]*ScheduleDef{
		"Local tz":   func() *ScheduleDef { d := onceDef(future, ""); d.TZ = "Local"; return d }(),
		"bogus tz":   func() *ScheduleDef { d := onceDef(future, ""); d.TZ = "Mars/Olympus"; return d }(),
		"offset tz":  func() *ScheduleDef { d := onceDef(future, ""); d.TZ = "+02:00"; return d }(),
		"empty tz":   func() *ScheduleDef { d := onceDef(future, ""); d.TZ = ""; return d }(),
		"dom0":       {When: When{Kind: WhenRepeat, Rule: &Rule{Freq: FreqMonthly, DOM: &dom0, H: 9}}, TZ: "UTC", Target: sessionTarget("s1")},
		"dom32":      {When: When{Kind: WhenRepeat, Rule: &Rule{Freq: FreqMonthly, DOM: &dom32, H: 9}}, TZ: "UTC", Target: sessionTarget("s1")},
		"past once":  onceDef(clock.Now(), ""),
		"no target":  {When: When{Kind: WhenOnce, At: future.UnixMilli()}, TZ: "UTC"},
		"bad late":   func() *ScheduleDef { d := onceDef(future, "later"); return d }(),
		"new no cwd": {When: When{Kind: WhenOnce, At: future.UnixMilli()}, TZ: "UTC", Target: Target{Kind: TargetNew, Project: "p", Model: "m"}},
	} {
		if _, err := r.Create(bg, CreateInput{Title: name, Schedule: def}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: create = %v, want invalid", name, err)
		}
	}
	if _, err := r.Create(bg, CreateInput{Title: "with assignee", Place: PlaceAgent, AssigneeSessionID: "s1", Schedule: onceDef(future, "")}); !errors.Is(err, ErrInvalid) {
		t.Errorf("template with assignee accepted: %v", err)
	}
	after, _ := r.Revision(bg)
	if after != before || countRows(t, r, "tasks", "1=1") != tasksBefore || countRows(t, r, "task_schedules", "1=1") != 1 {
		t.Fatalf("invalid input changed the database: rev %d->%d", before, after)
	}
}
