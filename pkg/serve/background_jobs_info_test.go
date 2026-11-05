package serve

import (
	"context"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
)

// rosterJobs waits for the bus to deliver the lifecycle events (subscribers
// run asynchronously) and returns the roster's count once it is stable.
func rosterJobs(t *testing.T, mgr *Manager, id string, want int) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		n := rosterJobsNow(t, mgr, id)
		if n == want || time.Now().After(deadline) {
			return n
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func rosterJobsNow(t *testing.T, mgr *Manager, id string) int {
	t.Helper()
	for _, info := range mgr.List() {
		if info.ID == id {
			return info.BackgroundJobs
		}
	}
	t.Fatalf("session %s not in roster", id)
	return 0
}

// A turn that ends with async subagents or bash jobs alive is idle, so the
// roster must say separately that jobs remain, and stop saying it when the
// last one ends.
func TestRosterReportsLiveBackgroundJobs(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()
	ownerWithSession(t, mgr, root, "Winerim")
	child := ownerChild(t, mgr, root, "child")
	b := child.runtime.Bus

	if n := rosterJobs(t, mgr, child.ID, 0); n != 0 {
		t.Fatalf("fresh session reports %d jobs", n)
	}
	b.Publish(bus.SubagentStarted{SessionID: child.ID, JobID: "a1", Async: true})
	b.Publish(bus.SubagentStarted{SessionID: child.ID, JobID: "a2", Async: true})
	b.Publish(bus.BashJobStarted{SessionID: child.ID, JobID: "b1", Command: "sleep 99"})
	if n := rosterJobs(t, mgr, child.ID, 3); n != 3 {
		t.Fatalf("jobs = %d, want 3", n)
	}
	if st := child.info().State; st != StateIdle {
		t.Fatalf("state = %s; jobs must not change the session state", st)
	}
	b.Publish(bus.SubagentEnded{SessionID: child.ID, JobID: "a1", Async: true, Status: "completed"})
	b.Publish(bus.SubagentEnded{SessionID: child.ID, JobID: "a2", Async: true, Status: "completed"})
	b.Publish(bus.BashJobSettled{SessionID: child.ID, JobID: "b1"})
	if n := rosterJobs(t, mgr, child.ID, 0); n != 0 {
		t.Fatalf("jobs = %d after all ended, want 0", n)
	}
}
