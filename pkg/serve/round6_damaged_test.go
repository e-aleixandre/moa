package serve

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

func round6Fixture(t *testing.T) (*schedHarness, *tasks.Repo) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	h := newSchedHarness(t, newMockProvider(), "2026-09-30T08:00:00Z")
	m := h.start()
	h.repos = append(h.repos, m.tasks)
	schedReviewStopWorkers(t, m)
	return h, h.repo()
}

// round6DamagedFile writes a session file cut short before its header ends,
// dated at mtime.
func round6DamagedFile(t *testing.T, base, name string, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(base, "stray")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(`{"id":"`+name), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestRound6OldDamagedFileDoesNotBlock(t *testing.T) {
	h, r := round6Fixture(t)
	o := readyNewRun(t, h, r, newTarget(t, h.root))
	round6DamagedFile(t, h.base, "old.json", time.UnixMilli(o.ObservedAt).Add(-time.Hour))
	provisioned := h.mgr.planner.provisionNew(bgc, o)
	after := occNow(t, r, o.ID)
	got := countSessions(t, h.base)
	t.Logf("old damaged file: provision=%t run=%s/%s sessions=%d", provisioned, after.State, after.Reason, got)
	if !provisioned || after.State != tasks.OccAssigned || got != 1 {
		t.Error("a damaged file older than the run blocked its session")
	}
}

func TestRound6NewerDamagedFileBlocksOnlyItsRun(t *testing.T) {
	h, r := round6Fixture(t)
	target := newTarget(t, h.root)
	a := readyNewRun(t, h, r, target)
	round6DamagedFile(t, h.base, "recent.json", time.UnixMilli(a.ObservedAt).Add(30*time.Second))
	b := readyNewRun(t, h, r, target)
	if b.ObservedAt <= a.ObservedAt+30_000 {
		t.Fatalf("test setup: run B observed at %d, not after the damaged file", b.ObservedAt)
	}
	okA := h.mgr.planner.provisionNew(bgc, a)
	okB := h.mgr.planner.provisionNew(bgc, b)
	afterA, afterB := occNow(t, r, a.ID), occNow(t, r, b.ID)
	got := countSessions(t, h.base)
	t.Logf("newer damaged file: A provision=%t %s/%s; B provision=%t %s/%s session=%q; sessions=%d",
		okA, afterA.State, afterA.Reason, okB, afterB.State, afterB.Reason, afterB.SessionID, got)
	if okA || afterA.State != tasks.OccFailed || afterA.Reason != reasonDestinationUncertain {
		t.Error("a damaged file newer than run A did not block it")
	}
	if !okB || afterB.State != tasks.OccAssigned || afterB.SessionID == "" || got != 1 {
		t.Error("a damaged file older than run B blocked it")
	}
}
