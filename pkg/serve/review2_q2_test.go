package serve

import (
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// materializeBlocked starts the planner's T0 and reports whether it is still
// waiting after a short while: it must wait for an admission in flight.
func materializeBlocked(t *testing.T, h *schedHarness) (<-chan struct{}, bool) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.mgr.planner.materialize(bgc)
	}()
	select {
	case <-done:
		return done, false
	case <-time.After(200 * time.Millisecond):
		return done, true
	}
}

// Q2: the next slot is coalesced only after the in-flight attempt ends. A
// reservation the IdleOnly gate refuses goes back to waiting, and is then
// superseded by the next slot: one pending assignment, not two.
func TestReview2ReservationRefusedAfterNextSlotLeavesOnePending(t *testing.T) {
	started, finishWork := make(chan struct{}, 1), make(chan struct{})
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(finishWork) }) }
	defer finish()
	h := newSchedHarness(t, newMockProvider(blockingHandler(started, finishWork, "busy work finished")), "2026-09-28T08:00:00Z")
	h.start()
	schedReviewStopWorkers(t, h.mgr)
	sess := h.session()
	r := h.repo()
	mkTemplate(t, r, "busy gate after reservation", dailyAt(9, 0, "UTC", toSession(sess.ID), tasks.Delivery{Busy: tasks.BusyWait}))
	h.clock.Set(mustUTC("2026-09-28T09:00:00Z"))
	due, err := r.MaterializeDue(bgc, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("T0: %+v %v", due, err)
	}
	old, err := r.AssignOccurrence(bgc, due[0].ID, tasks.Destination{SessionID: sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := noticeByID(t, r, old.NoticeID)
	reserved, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	h.mgr.planner.hooks.beforeAdmit = func(string) { close(reserved); <-release }
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.mgr.notices.mu.Lock()
		defer h.mgr.notices.mu.Unlock()
		h.mgr.notices.attempt(bgc, snapshot, true)
	}()
	select {
	case <-reserved:
	case <-time.After(5 * time.Second):
		t.Fatal("reservation timeout")
	}
	if err := sess.runtime.Bus.Execute(bus.SendPrompt{Text: "work starts between reservation and admission"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("busy work did not start")
	}
	h.clock.Set(mustUTC("2026-09-29T09:00:00Z"))
	materialized, blocked := materializeBlocked(t, h)
	unblock()
	for _, ch := range []<-chan struct{}{done, materialized} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("attempt or T0 did not return")
		}
	}
	if !blocked {
		t.Error("T0 coalesced while an admission was in flight")
	}
	runs := runsOfT(t, r, old.ScheduleTaskID)
	if len(runs) != 2 {
		t.Fatalf("runs = %+v", runs)
	}
	if _, err := r.AssignOccurrence(bgc, runs[1].ID, tasks.Destination{SessionID: sess.ID}); err != nil {
		t.Fatal(err)
	}
	got := occNow(t, r, old.ID)
	n := noticeByID(t, r, old.NoticeID)
	pending := sqlCount(t, h.dbPath(), "SELECT count(*) FROM task_notifications WHERE state IN ('pending','held')")
	t.Logf("old=%s/%s admitted=%d; notice=%s/%s; pending notices=%d", got.State, got.Reason, got.AdmittedAt, n.State, n.Reason, pending)
	if got.AdmittedAt != 0 {
		t.Fatal("setup admitted an idle-only assignment into busy session")
	}
	if pending != 1 || got.State != tasks.OccSkipped || got.Reason != tasks.ReasonSuperseded {
		t.Fatalf("Q2: refused reservation survived the next slot (%s/%s), leaving %d undelivered assignments", got.State, got.Reason, pending)
	}
}

// Q2 control: an assignment admitted while the next slot waits is kept, and
// the new run does not count it as missed.
func TestReview2CoalescingKeepsReservedDelivery(t *testing.T) {
	prov := newMockProvider(simpleResponseHandler("reserved assignment"))
	h := newSchedHarness(t, prov, "2026-09-28T08:00:00Z")
	h.start()
	schedReviewStopWorkers(t, h.mgr)
	sess := h.session()
	r := h.repo()
	tmpl := mkTemplate(t, r, "reserved across next slot", dailyAt(9, 0, "UTC", toSession(sess.ID), tasks.Delivery{}))
	h.clock.Set(mustUTC("2026-09-28T09:00:00Z"))
	due, err := r.MaterializeDue(bgc, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("T0: %+v %v", due, err)
	}
	old, err := r.AssignOccurrence(bgc, due[0].ID, tasks.Destination{SessionID: sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	reserved, release := make(chan struct{}), make(chan struct{})
	h.mgr.planner.hooks.beforeAdmit = func(string) { close(reserved); <-release }
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.mgr.notices.mu.Lock()
		defer h.mgr.notices.mu.Unlock()
		h.mgr.notices.attempt(bgc, noticeByID(t, r, old.NoticeID), true)
	}()
	select {
	case <-reserved:
	case <-time.After(5 * time.Second):
		t.Fatal("reservation timeout")
	}
	h.clock.Set(mustUTC("2026-09-29T09:00:00Z"))
	materialized, blocked := materializeBlocked(t, h)
	close(release)
	<-done
	<-materialized
	if !blocked {
		t.Error("T0 coalesced while an admission was in flight")
	}
	got := occNow(t, r, old.ID)
	runs := runsOfT(t, r, tmpl.ID)
	if len(runs) != 2 {
		t.Fatalf("runs=%+v", runs)
	}
	if got.State != tasks.OccAssigned || got.ChildTaskID == 0 || got.AdmittedAt == 0 || runs[1].MissedCount != 0 {
		t.Fatalf("reserved assignment lost: old=%+v latest=%+v", got, runs[1])
	}
	pollUntil(t, 5*time.Second, "provider call", func() bool { return prov.calls.Load() == 1 })
}

func TestReview2StalePendingSnapshotCannotDeliverSupersededRun(t *testing.T) {
	prov := newMockProvider(simpleResponseHandler("must not run stale assignment"))
	h := newSchedHarness(t, prov, "2026-09-28T08:00:00Z")
	h.start()
	schedReviewStopWorkers(t, h.mgr)
	sess := h.session()
	r := h.repo()
	mkTemplate(t, r, "stale dispatcher snapshot", dailyAt(9, 0, "UTC", toSession(sess.ID), tasks.Delivery{}))
	h.clock.Set(mustUTC("2026-09-28T09:00:00Z"))
	due, err := r.MaterializeDue(bgc, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("T0: %+v %v", due, err)
	}
	old, err := r.AssignOccurrence(bgc, due[0].ID, tasks.Destination{SessionID: sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := noticeByID(t, r, old.NoticeID)
	h.clock.Set(mustUTC("2026-09-29T09:00:00Z"))
	latest, err := r.MaterializeDue(bgc, 10)
	if err != nil || len(latest) != 1 {
		t.Fatalf("next T0: %+v %v", latest, err)
	}
	h.mgr.notices.mu.Lock()
	h.mgr.notices.attempt(bgc, snapshot, true)
	h.mgr.notices.mu.Unlock()
	if got := occNow(t, r, old.ID); got.State != tasks.OccSkipped || got.Reason != tasks.ReasonSuperseded || got.AdmittedAt != 0 || prov.calls.Load() != 0 {
		t.Fatalf("stale snapshot admitted superseded run: %+v calls=%d", got, prov.calls.Load())
	}
	if hasNotice(sess.History(), snapshot.ID) {
		t.Fatal("superseded notice in history")
	}
}

// Q2: before (re)trying a recurring run's assignment, a newer slot already
// due wins: the old one is not delivered, and the next T0 supersedes it.
func TestRound3AttemptYieldsToADueSlot(t *testing.T) {
	prov := newMockProvider(simpleResponseHandler("old run delivered"))
	h := newSchedHarness(t, prov, "2026-09-28T08:00:00Z")
	h.start()
	schedReviewStopWorkers(t, h.mgr)
	sess := h.session()
	r := h.repo()
	mkTemplate(t, r, "retry after next slot", dailyAt(9, 0, "UTC", toSession(sess.ID), tasks.Delivery{Busy: tasks.BusyWait}))
	h.clock.Set(mustUTC("2026-09-28T09:00:00Z"))
	due, err := r.MaterializeDue(bgc, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("T0: %+v %v", due, err)
	}
	old, err := r.AssignOccurrence(bgc, due[0].ID, tasks.Destination{SessionID: sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	// It waited for idle; the idle event comes after the next slot is due
	// but before the planner took it.
	h.clock.Set(mustUTC("2026-09-29T09:00:01Z"))
	h.mgr.notices.mu.Lock()
	h.mgr.notices.attempt(bgc, noticeByID(t, r, old.NoticeID), true)
	h.mgr.notices.mu.Unlock()
	if n := noticeByID(t, r, old.NoticeID); n.State != tasks.NoticePending || prov.calls.Load() != 0 || hasNotice(sess.History(), n.ID) {
		t.Fatalf("superseded-to-be run delivered: notice %s/%s calls=%d", n.State, n.Reason, prov.calls.Load())
	}
	h.mgr.planner.materialize(bgc)
	if got := occNow(t, r, old.ID); got.State != tasks.OccSkipped || got.Reason != tasks.ReasonSuperseded {
		t.Fatalf("old run = %s/%s, want superseded", got.State, got.Reason)
	}
}
