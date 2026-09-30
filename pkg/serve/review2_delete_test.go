package serve

import (
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

func review2MoaAssignedRun(t *testing.T, h *schedHarness, r *tasks.Repo, sid string) tasks.Occurrence {
	t.Helper()
	mkTemplate(t, r, "keep this assignment on a refused delete", onceAt(h.clock.Now().Add(time.Minute), toSession(sid), tasks.Delivery{Saved: tasks.DeliverHold}))
	h.clock.Advance(time.Minute)
	due, err := r.MaterializeDue(bgc, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("setup T0: %+v / %v", due, err)
	}
	o, err := r.AssignOccurrence(bgc, due[0].ID, tasks.Destination{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// The new SQLite refusal path must not erase a still-live automation run's
// idempotency key: an ordinary webhook retry would otherwise run twice.
func TestReview2MoaDeleteSQLRefusalKeepsAutomationKey(t *testing.T) {
	h := newSchedHarness(t, newMockProvider(simpleResponseHandler("ok")), "2026-09-30T08:00:00Z")
	m := h.start()
	schedReviewStopWorkers(t, m)
	req := AutomationRunRequest{Prompt: "deploy once", IdempotencyKey: "review2-deploy-once", CWD: h.root}
	sid, created, err := m.CreateAutomationRun(req)
	if err != nil || !created {
		t.Fatalf("setup automation: %s / %t / %v", sid, created, err)
	}
	r := h.repo()
	o := review2MoaAssignedRun(t, h, r, sid)
	drop := abortTrigger(t, h.dbPath(), "review2_refuse_settle", "BEFORE UPDATE ON task_occurrences")
	defer drop()
	if err := m.Delete(sid); err == nil {
		t.Fatal("test setup: Delete did not hit the SQLite refusal")
	}
	if _, live := m.Get(sid); !live || occNow(t, r, o.ID).State != tasks.OccAssigned {
		t.Fatal("test setup: refused Delete did not retain the session and its run")
	}
	retryID, retryCreated, err := m.CreateAutomationRun(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("original=%s retry=%s created=%t sessions=%d", sid, retryID, retryCreated, countSessions(t, h.base))
	if retryCreated || retryID != sid {
		t.Errorf("refused Delete lost idempotency: retry created=%t, session=%s, want original=%s", retryCreated, retryID, sid)
	}
}
