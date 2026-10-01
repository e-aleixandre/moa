package serve

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func round5Fixture(t *testing.T) (*schedHarness, *tasks.Repo, tasks.Occurrence, string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	h := newSchedHarness(t, newMockProvider(), "2026-09-30T08:00:00Z")
	m := h.start()
	h.repos = append(h.repos, m.tasks)
	schedReviewStopWorkers(t, m)
	r := h.repo()
	o := readyNewRun(t, h, r, newTarget(t, h.root))
	sid := round7Created(t, h, r, o, "", false)
	store, err := session.FindSessionStoreReadOnly(h.base, sid)
	if err != nil {
		t.Fatal(err)
	}
	return h, r, o, sid, filepath.Join(store.Dir(), sid+".json")
}

// round5Restart runs a new Manager's real startup recovery and stops it
// before the planner's first pass can change the state under inspection.
func round5Restart(t *testing.T, h *schedHarness) *Manager {
	t.Helper()
	h.stop()
	h.hooks.beforePass = func(ctx context.Context) { <-ctx.Done() }
	m := h.start()
	h.repos = append(h.repos, m.tasks)
	schedReviewStopWorkers(t, m)
	return m
}

// round5TruncateAfterID cuts a session file right after its ID, before its
// metadata and the transcript boundary were written.
func round5TruncateAfterID(t *testing.T, path, sid string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	at := bytes.Index(raw, []byte(sid))
	if at < 0 {
		t.Fatal("session ID absent from fixture")
	}
	if err := os.WriteFile(path, raw[:at+len(sid)+1], 0o600); err != nil {
		t.Fatal(err)
	}
}

// A delete intent whose file is still there, even damaged, was never
// unlinked: the start withdraws it, and the damaged file still stops its
// run instead of letting it create another session.
func TestRound5TruncatedHeaderRecoveryKeepsMark(t *testing.T) {
	h, r, o, sid, path := round5Fixture(t)
	if err := r.MarkSessionDiscarding(bgc, sid); err != nil {
		t.Fatal(err)
	}
	round5TruncateAfterID(t, path, sid)
	m := round5Restart(t, h)
	ds, err := r.SessionDiscards(bgc)
	if err != nil {
		t.Fatal(err)
	}
	provisioned := m.planner.provisionNew(bgc, o)
	_, fileErr := os.Stat(path)
	files := round7Files(t, h.base)
	after := occNow(t, r, o.ID)
	t.Logf("startup with incomplete header: marks=%d file=%v provision=%t run=%s/%s files=%d", len(ds), fileErr, provisioned, after.State, after.Reason, files)
	if len(ds) != 0 || fileErr != nil || provisioned || files != 1 || after.Reason != reasonDestinationUncertain {
		t.Error("startup kept the intent of a delete that never unlinked, or the run made another session")
	}
}

func TestRound5RepeatedRecoveryKeepsDeleteIntent(t *testing.T) {
	h, r, o, sid, path := round5Fixture(t)
	closeSession(t, h.mgr, sid)
	drop := abortTrigger(t, h.dbPath(), "round5_settlement_abort", "BEFORE UPDATE ON task_occurrences WHEN NEW.reason = 'session_deleted'")
	if err := h.mgr.Delete(sid); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file not unlinked: %v", err)
	}
	h.clock.Advance(12 * time.Minute)
	round5Restart(t, h)
	first := occNow(t, r, o.ID)
	if ds, err := r.SessionDiscards(bgc); err != nil || len(ds) != 1 {
		t.Fatalf("first startup lost the mark: %+v %v", ds, err)
	}
	drop()
	m := round5Restart(t, h)
	second := occNow(t, r, o.ID)
	ds, _ := r.SessionDiscards(bgc)
	provisioned := m.planner.provisionNew(bgc, second)
	t.Logf("first start=%s/%s second start=%s/%s marks=%d provision=%t", first.State, first.Reason, second.State, second.Reason, len(ds), provisioned)
	if second.State != tasks.OccFailed || second.Reason != tasks.ReasonSessionDeleted || len(ds) != 0 || provisioned {
		t.Error("a second startup lost the durable delete intent")
	}
}

// A run linked to a discard mark is settled at whatever gate it waits:
// late after a re-gate, or late with an uncertain delivery.
func TestRound5SettlementCoversLateRuns(t *testing.T) {
	for _, c := range []struct{ reason, wantState, wantReason string }{
		{tasks.ReasonRegated, tasks.OccFailed, tasks.ReasonSessionDeleted},
		{tasks.ReasonUncertain, tasks.OccSkipped, tasks.ReasonDeletedDuringDeliver},
	} {
		t.Run(c.reason, func(t *testing.T) {
			h, r, o, sid, path := round5Fixture(t)
			closeSession(t, h.mgr, sid)
			if err := r.MarkSessionDiscarding(bgc, sid); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			sqlExec(t, h.dbPath(), "UPDATE task_occurrences SET state = 'late', reason = ? WHERE id = ?", c.reason, o.ID)
			round5Restart(t, h)
			after := occNow(t, r, o.ID)
			ds, _ := r.SessionDiscards(bgc)
			t.Logf("late/%s at startup: run=%s/%s marks=%d", c.reason, after.State, after.Reason, len(ds))
			if after.State != c.wantState || after.Reason != c.wantReason || len(ds) != 0 {
				t.Errorf("settlement left a marked late run unsettled")
			}
		})
	}
}
