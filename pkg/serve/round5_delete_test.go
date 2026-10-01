package serve

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

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
	sid := markedSession(t, h, o.ID, o.ScheduleTaskID)
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

func TestRound5DeleteTruncatedHeaderFailsClosed(t *testing.T) {
	h, r, o, sid, path := round5Fixture(t)
	round5TruncateAfterID(t, path, sid)
	_, headerErr := session.FindSummaryReadOnly(h.base, sid)
	deleteErr := h.mgr.Delete(sid)
	_, fileErr := os.Stat(path)
	provisioned := h.mgr.planner.provisionNew(bgc, o)
	after := occNow(t, r, o.ID)
	files := round5SessionFiles(t, h.base)
	t.Logf("truncated header: header=%v Delete=%v file=%v provision=%t run=%s/%s files=%d", headerErr, deleteErr, fileErr, provisioned, after.State, after.Reason, files)
	// The uncertain scan fails the run like any other unverifiable destination.
	if headerErr == nil || deleteErr == nil || fileErr != nil || provisioned || files != 1 ||
		after.State != tasks.OccFailed || after.Reason != reasonDestinationUncertain {
		t.Error("an incomplete header was treated as an unmarked session")
	}
}

func TestRound5TruncatedHeaderRecoveryKeepsMark(t *testing.T) {
	h, r, o, sid, path := round5Fixture(t)
	if err := r.MarkSessionDiscarding(bgc, sid, o.ID); err != nil {
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
	files := round5SessionFiles(t, h.base)
	t.Logf("startup with incomplete header: marks=%d file=%v provision=%t files=%d", len(ds), fileErr, provisioned, files)
	if len(ds) != 1 || fileErr != nil || provisioned || files != 1 {
		t.Error("startup cleared the mark of an unreadable header and created another session for the run")
	}
}

// round5SessionFiles counts session files on disk, readable or not.
func round5SessionFiles(t *testing.T, base string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(base, "*", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}
