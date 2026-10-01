package serve

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func round7Fixture(t *testing.T) (*schedHarness, *tasks.Repo) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	h := newSchedHarness(t, newMockProvider(), "2026-09-30T08:00:00Z")
	m := h.start()
	h.repos = append(h.repos, m.tasks)
	schedReviewStopWorkers(t, m)
	return h, h.repo()
}

// round7Reserve records a fresh session ID for run o, as the planner does
// before creating its session.
func round7Reserve(t *testing.T, r *tasks.Repo, o tasks.Occurrence) string {
	t.Helper()
	id, err := session.NewID()
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.ReserveSession(bgc, o.ID, id)
	if err != nil || got != id {
		t.Fatalf("reserve = %q, %v", got, err)
	}
	return id
}

// round7Created leaves run o as a crash after its session's first save
// leaves it: reserved, created, not bound. live keeps the session loaded.
func round7Created(t *testing.T, h *schedHarness, r *tasks.Repo, o tasks.Occurrence, title string, live bool) string {
	t.Helper()
	id := round7Reserve(t, r, o)
	if _, err := h.mgr.CreateSession(CreateOpts{Title: title, CWD: o.Spec.Target.CWD, sessionID: id}); err != nil {
		t.Fatal(err)
	}
	if !live {
		closeSession(t, h.mgr, id)
	}
	return id
}

// round7Path is where run o's reserved session id is stored.
func round7Path(t *testing.T, h *schedHarness, o tasks.Occurrence, id string) string {
	t.Helper()
	store, err := session.OpenFileStoreReadOnly(h.base, o.Spec.Target.CWD)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(store.Dir(), id+".json")
}

// round7Files counts session files on disk, readable or not.
func round7Files(t *testing.T, base string) int {
	t.Helper()
	var n int
	for _, pat := range []string{"*.json", "*/*.json"} {
		m, err := filepath.Glob(filepath.Join(base, pat))
		if err != nil {
			t.Fatal(err)
		}
		n += len(m)
	}
	return n
}

// round7Write puts body at path, dated at mtime when not zero.
func round7Write(t *testing.T, path string, body []byte, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
}

// A crash after the reservation and before the session's creation: the
// retry creates the session with the reserved ID, never another.
func TestRound7ReservedBeforeCreate(t *testing.T) {
	h, r := round7Fixture(t)
	o := readyNewRun(t, h, r, newTarget(t, h.root))
	id := round7Reserve(t, r, o)
	ok := h.mgr.planner.provisionNew(bgc, o)
	got := occNow(t, r, o.ID)
	t.Logf("reserved, not created: provision=%t run=%s session=%q reserved=%q files=%d", ok, got.State, got.SessionID, id, round7Files(t, h.base))
	if !ok || got.State != tasks.OccAssigned || got.SessionID != id || round7Files(t, h.base) != 1 {
		t.Fatal("the run's session was not created with its reserved ID")
	}
	if _, err := os.Stat(round7Path(t, h, o, id)); err != nil {
		t.Fatalf("reserved session not at its path: %v", err)
	}
}

// A crash after the first save and before T1: the run binds to the file
// already there, saved or still loaded.
func TestRound7CreatedBeforeT1(t *testing.T) {
	for _, live := range []bool{false, true} {
		name := "saved"
		if live {
			name = "live"
		}
		t.Run(name, func(t *testing.T) {
			h, r := round7Fixture(t)
			o := readyNewRun(t, h, r, newTarget(t, h.root))
			id := round7Created(t, h, r, o, "fresh start", live)
			ok := h.mgr.planner.provisionNew(bgc, o)
			got := occNow(t, r, o.ID)
			t.Logf("%s, before T1: provision=%t run=%s session=%q reserved=%q files=%d", name, ok, got.State, got.SessionID, id, round7Files(t, h.base))
			if !ok || got.SessionID != id || round7Files(t, h.base) != 1 {
				t.Fatal("the run did not bind to the session already created for it")
			}
		})
	}
}

// A session being resumed under the reserved ID is waited for, not
// replaced or duplicated.
func TestRound7ReservedSessionBeingResumed(t *testing.T) {
	h, r := round7Fixture(t)
	o := readyNewRun(t, h, r, newTarget(t, h.root))
	id := round7Created(t, h, r, o, "fresh start", false)
	path := round7Path(t, h, o, id)
	before, _ := os.ReadFile(path)
	h.mgr.mu.Lock()
	h.mgr.resuming[id] = struct{}{}
	h.mgr.mu.Unlock()
	ok := h.mgr.planner.provisionNew(bgc, o)
	during := occNow(t, r, o.ID)
	after, _ := os.ReadFile(path)
	t.Logf("resuming: provision=%t run=%s/%s files=%d bytes-kept=%t", ok, during.State, during.Reason, round7Files(t, h.base), bytes.Equal(before, after))
	if ok || during.State != tasks.OccReady || round7Files(t, h.base) != 1 || !bytes.Equal(before, after) {
		t.Fatal("a session being resumed was replaced, duplicated or failed its run")
	}
	h.mgr.mu.Lock()
	delete(h.mgr.resuming, id)
	h.mgr.mu.Unlock()
	if !h.mgr.planner.provisionNew(bgc, o) || occNow(t, r, o.ID).SessionID != id {
		t.Fatalf("after the resume: %+v", occNow(t, r, o.ID))
	}
}

// A damaged file at the reserved path is the run's session: only that run
// stops, its bytes stay, whatever its title says. Other runs go on.
func TestRound7DamagedReservedFileBlocksOnlyItsRun(t *testing.T) {
	for _, title := range []string{"entries", "messages", "plain"} {
		t.Run(title, func(t *testing.T) {
			h, r := round7Fixture(t)
			target := newTarget(t, h.root)
			a := readyNewRun(t, h, r, target)
			id := round7Created(t, h, r, a, title, false)
			path := round7Path(t, h, a, id)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// Cut right after the title, before the metadata.
			at := bytes.Index(raw, []byte(`"title":"`+title+`"`))
			if at < 0 {
				t.Fatalf("title absent from %s", raw)
			}
			cut := raw[:at+len(`"title":"`+title+`"`)]
			round7Write(t, path, cut, time.Time{})
			b := readyNewRun(t, h, r, target)
			okA := h.mgr.planner.provisionNew(bgc, a)
			okB := h.mgr.planner.provisionNew(bgc, b)
			ga, gb := occNow(t, r, a.ID), occNow(t, r, b.ID)
			kept, _ := os.ReadFile(path)
			t.Logf("damaged %q: A provision=%t %s/%s; B provision=%t %s session=%q; files=%d bytes-kept=%t",
				title, okA, ga.State, ga.Reason, okB, gb.State, gb.SessionID, round7Files(t, h.base), bytes.Equal(kept, cut))
			if okA || ga.State != tasks.OccFailed || ga.Reason != reasonDestinationUncertain || !bytes.Equal(kept, cut) {
				t.Error("run A was not stopped by its damaged session, or the file changed")
			}
			if !okB || gb.State != tasks.OccAssigned || gb.SessionID == "" || gb.SessionID == id || round7Files(t, h.base) != 2 {
				t.Error("run B was blocked by another run's damaged session")
			}
		})
	}
}

// Damaged files under other IDs, old or new, marker-looking or not, are
// nobody's run: they never block one.
func TestRound7OtherDamagedFilesDoNotBlock(t *testing.T) {
	h, r := round7Fixture(t)
	o := readyNewRun(t, h, r, newTarget(t, h.root))
	other, _ := session.NewID()
	round7Write(t, filepath.Join(h.base, "stray", "old.json"), []byte(`{"id":"old`), h.clock.Now().Add(-time.Hour))
	round7Write(t, filepath.Join(h.base, "stray", other+".json"), []byte(`{"id":"`+other+`","metadata":{"scheduled_occurrence_id":"1"`), time.Now().Add(time.Hour))
	round7Write(t, filepath.Join(h.base, "stray", "entries.json"), []byte(`{"id":"x","title":"entries"`), time.Now())
	ok := h.mgr.planner.provisionNew(bgc, o)
	got := occNow(t, r, o.ID)
	t.Logf("unrelated damaged files: provision=%t run=%s/%s files=%d", ok, got.State, got.Reason, round7Files(t, h.base))
	if !ok || got.State != tasks.OccAssigned || round7Files(t, h.base) != 4 {
		t.Fatal("an unrelated damaged file blocked the run")
	}
}

// A file at the reserved path whose header names another session is not
// the run's session, and is not replaced: the run stops.
func TestRound7ReservedPathHeaderMismatch(t *testing.T) {
	h, r := round7Fixture(t)
	o := readyNewRun(t, h, r, newTarget(t, h.root))
	id := round7Reserve(t, r, o)
	other, _ := session.NewID()
	path := round7Path(t, h, o, id)
	body := []byte(`{"id":"` + other + `","version":2,"entries":[]}`)
	round7Write(t, path, body, time.Time{})
	ok := h.mgr.planner.provisionNew(bgc, o)
	got := occNow(t, r, o.ID)
	kept, _ := os.ReadFile(path)
	t.Logf("header mismatch: provision=%t run=%s/%s files=%d bytes-kept=%t", ok, got.State, got.Reason, round7Files(t, h.base), bytes.Equal(kept, body))
	if ok || got.State != tasks.OccFailed || got.Reason != reasonDestinationUncertain || !bytes.Equal(kept, body) || round7Files(t, h.base) != 1 {
		t.Fatal("a file naming another session was used or replaced")
	}
}

// Deleting the run's session before T1 settles the run by its reservation:
// nothing reads the file, and nothing recreates it.
func TestRound7DeleteBeforeBindingSettlesByReservation(t *testing.T) {
	for _, damaged := range []bool{false, true} {
		name := "intact"
		if damaged {
			name = "damaged"
		}
		t.Run(name, func(t *testing.T) {
			h, r := round7Fixture(t)
			o := readyNewRun(t, h, r, newTarget(t, h.root))
			id := round7Created(t, h, r, o, "fresh start", false)
			if damaged {
				round7Write(t, round7Path(t, h, o, id), []byte(`{"id":"`+id+`"`), time.Time{})
			}
			err := h.mgr.Delete(id)
			ok := h.mgr.planner.provisionNew(bgc, o)
			got := occNow(t, r, o.ID)
			t.Logf("%s delete: err=%v provision=%t run=%s/%s files=%d", name, err, ok, got.State, got.Reason, round7Files(t, h.base))
			if err != nil || ok || got.State != tasks.OccFailed || got.Reason != tasks.ReasonSessionDeleted || round7Files(t, h.base) != 0 {
				t.Fatal("delete did not settle the run by its reservation, or the session came back")
			}
		})
	}
}

// A delete whose settlement fails after the unlink keeps the run from
// recreating the session, before and after a restart.
func TestRound7DeleteLostSettlementNoResurrection(t *testing.T) {
	h, r := round7Fixture(t)
	o := readyNewRun(t, h, r, newTarget(t, h.root))
	id := round7Created(t, h, r, o, "fresh start", false)
	drop := abortTrigger(t, h.dbPath(), "round7_lost_settlement", "BEFORE UPDATE OF revision ON tasks_meta")
	err := h.mgr.Delete(id)
	drop()
	ok := h.mgr.planner.provisionNew(bgc, o)
	mid := occNow(t, r, o.ID)
	t.Logf("lost settlement: Delete=%v provision=%t run=%s/%s files=%d", err, ok, mid.State, mid.Reason, round7Files(t, h.base))
	if ok || round7Files(t, h.base) != 0 {
		t.Fatal("the deleted session was recreated before its settlement")
	}
	h.clock.Advance(12 * time.Minute)
	m := round5Restart(t, h)
	got := occNow(t, r, o.ID)
	ok = m.planner.provisionNew(bgc, got)
	t.Logf("after restart: run=%s/%s provision=%t files=%d", got.State, got.Reason, ok, round7Files(t, h.base))
	if got.State != tasks.OccFailed || got.Reason != tasks.ReasonSessionDeleted || ok || round7Files(t, h.base) != 0 {
		t.Fatal("restart did not settle the deleted run, or recreated its session")
	}
}

// A re-gated run keeps its reservation: the owner's Run binds it to the
// session it already created.
func TestRound7RegateKeepsReservation(t *testing.T) {
	h, r := round7Fixture(t)
	o := readyNewRun(t, h, r, newTarget(t, h.root))
	id := round7Created(t, h, r, o, "fresh start", false)
	h.clock.Advance(12 * time.Minute)
	m := round5Restart(t, h)
	late := occNow(t, r, o.ID)
	if late.State != tasks.OccLate || late.ReservedSessionID != id {
		t.Fatalf("after restart = %+v", late)
	}
	ready, err := r.ConfirmOccurrence(bgc, o.ID, late.Revision, tasks.LateRun)
	if err != nil {
		t.Fatal(err)
	}
	ok := m.planner.provisionNew(bgc, ready)
	got := occNow(t, r, o.ID)
	t.Logf("regated then run: provision=%t run=%s session=%q reserved=%q files=%d", ok, got.State, got.SessionID, id, round7Files(t, h.base))
	if !ok || got.SessionID != id || round7Files(t, h.base) != 1 {
		t.Fatal("the owner's Run created another session")
	}
}

// A failed run sent to another session B: deleting its original A does not
// touch it; deleting B settles it.
func TestRound7RerouteThenDeleteOriginal(t *testing.T) {
	h, r := round7Fixture(t)
	o := readyNewRun(t, h, r, newTarget(t, h.root))
	a := round7Created(t, h, r, o, "fresh start", false)
	round7Write(t, round7Path(t, h, o, a), []byte(`{"id":"`+a+`"`), time.Time{})
	if h.mgr.planner.provisionNew(bgc, o) {
		t.Fatal("test setup: damaged session was used")
	}
	failed := occNow(t, r, o.ID)
	b := h.savedSession()
	dest, reason, err := h.mgr.sessionDestination(b)
	if err != nil || reason != "" {
		t.Fatal(reason, err)
	}
	if _, err := r.RerouteOccurrence(bgc, o.ID, failed.Revision, dest); err != nil {
		t.Fatal(err)
	}
	errA := h.mgr.Delete(a)
	afterA := occNow(t, r, o.ID)
	t.Logf("delete original A: err=%v run=%s session=%q", errA, afterA.State, afterA.SessionID)
	if errA != nil || afterA.State != tasks.OccAssigned || afterA.SessionID != b {
		t.Fatal("deleting the original session touched the rerouted run")
	}
	if err := h.mgr.Delete(b); err != nil {
		t.Fatal(err)
	}
	if got := occNow(t, r, o.ID); got.State != tasks.OccFailed || got.Reason != tasks.ReasonSessionDeleted {
		t.Fatalf("deleting B = %+v", got)
	}
}
