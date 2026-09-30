package serve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

var bgc = context.Background()

// assignedAndDelivered waits until a run's assignment reached its session's
// saved transcript exactly once.
func assignedAndDelivered(t *testing.T, h *schedHarness, r *tasks.Repo, occID int64) tasks.Occurrence {
	t.Helper()
	o := waitOcc(t, r, occID, "run assigned", func(o tasks.Occurrence) bool { return o.State == tasks.OccAssigned && o.NoticeID != "" })
	waitNoticeByID(t, r, o.NoticeID, tasks.NoticeDelivered)
	o = waitOcc(t, r, occID, "run admitted", func(o tasks.Occurrence) bool { return o.AdmittedAt != 0 })
	if got := transcriptNoticeCount(t, h.base, o.SessionID, o.NoticeID); got != 1 {
		t.Fatalf("assignment saved %d times", got)
	}
	return o
}

// A run authorized on time (T0 ready) whose process stopped before T1 is
// assigned once after a restart, under its original slot.
func TestTaskSchedulerRestartReady(t *testing.T) {
	h := newSchedHarness(t, newMockProvider(simpleResponseHandler("on it")), "2026-09-30T08:00:00Z")
	h.start()
	sid := h.savedSession()
	h.stop()
	r := h.repo()
	tmpl := mkTemplate(t, r, "backup", onceAt(h.clock.Now().Add(time.Minute), toSession(sid), tasks.Delivery{}))
	h.clock.Advance(time.Minute)
	ready, err := r.MaterializeDue(bgc, 10)
	if err != nil || len(ready) != 1 || ready[0].State != tasks.OccReady {
		t.Fatalf("T0 = %+v, %v", ready, err)
	}
	h.clock.Advance(3 * time.Minute)

	h.start()
	h.pass()
	o := assignedAndDelivered(t, h, r, ready[0].ID)
	if o.DueAt != ready[0].DueAt || o.SessionID != sid {
		t.Fatalf("run = %+v", o)
	}
	oneRun(t, r, tmpl.ID)
	if n := sqlCount(t, h.dbPath(), "SELECT COUNT(*) FROM tasks WHERE place = 'agent'"); n != 1 {
		t.Fatalf("%d children", n)
	}
	if n := sqlCount(t, h.dbPath(), "SELECT COUNT(*) FROM task_notifications"); n != 1 {
		t.Fatalf("%d notices", n)
	}
	h.pass()
	oneRun(t, r, tmpl.ID)
}

// A slot never observed before a long stop is late at first observation:
// nothing runs, no session wakes, until the owner says Run — once.
func TestTaskSchedulerRestartOverdue(t *testing.T) {
	prov := newMockProvider(simpleResponseHandler("on it"))
	h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
	h.start()
	sid := h.savedSession()
	h.stop()
	r := h.repo()
	tmpl := mkTemplate(t, r, "report", onceAt(h.clock.Now().Add(time.Minute), toSession(sid), tasks.Delivery{}))
	h.clock.Advance(time.Minute + tasks.LateAfter)

	h.start()
	h.pass()
	o := oneRun(t, r, tmpl.ID)
	if o.State != tasks.OccLate || o.ChildTaskID != 0 || o.NoticeID != "" {
		t.Fatalf("overdue run = %+v", o)
	}
	h.pass()
	if _, live := h.mgr.Get(sid); live || prov.calls.Load() != 0 {
		t.Fatalf("an overdue run woke its session (live=%v, calls=%d)", live, prov.calls.Load())
	}
	if n := sqlCount(t, h.dbPath(), "SELECT COUNT(*) FROM task_notifications"); n != 0 {
		t.Fatalf("%d notices before confirmation", n)
	}
	if _, err := r.ConfirmOccurrence(bgc, o.ID, o.Revision, tasks.LateRun); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ConfirmOccurrence(bgc, o.ID, o.Revision, tasks.LateRun); err == nil {
		t.Fatal("a retried confirmation succeeded")
	}
	h.mgr.planner.nudge()
	h.pass()
	assignedAndDelivered(t, h, r, o.ID)
	if n := sqlCount(t, h.dbPath(), "SELECT COUNT(*) FROM tasks WHERE place = 'agent'"); n != 1 {
		t.Fatalf("%d children after Run", n)
	}
}

// Runs the owner already authorized (confirmed, Run now) and late=run runs
// are never gated again by a restart, however long it took. A reservation
// alone is not an admission: see TestScheduleReviewReservedNotAdmittedIsRegated.
func TestTaskSchedulerRestartDoesNotRelateAuthorizedRun(t *testing.T) {
	cases := map[string]func(t *testing.T, h *schedHarness, r *tasks.Repo, sid string) int64{
		"confirmed": func(t *testing.T, h *schedHarness, r *tasks.Repo, sid string) int64 {
			tmpl := mkTemplate(t, r, "confirmed", onceAt(h.clock.Now().Add(time.Minute), toSession(sid), tasks.Delivery{}))
			h.clock.Advance(time.Minute + tasks.LateAfter)
			os, _ := r.MaterializeDue(bgc, 10)
			if len(os) != 1 || os[0].State != tasks.OccLate {
				t.Fatalf("T0 = %+v", os)
			}
			if _, err := r.ConfirmOccurrence(bgc, os[0].ID, os[0].Revision, tasks.LateRun); err != nil {
				t.Fatal(err)
			}
			return tmpl.ID
		},
		"run_now": func(t *testing.T, h *schedHarness, r *tasks.Repo, sid string) int64 {
			tmpl := mkTemplate(t, r, "run now", onceAt(h.clock.Now().Add(time.Hour), toSession(sid), tasks.Delivery{}))
			if _, err := r.RunNow(bgc, tmpl.ID, tmpl.Revision, tmpl.Next); err != nil {
				t.Fatal(err)
			}
			return tmpl.ID
		},
		"late_run_policy": func(t *testing.T, h *schedHarness, r *tasks.Repo, sid string) int64 {
			tmpl := mkTemplate(t, r, "late run", onceAt(h.clock.Now().Add(time.Minute), toSession(sid), tasks.Delivery{Late: tasks.LateRun}))
			h.clock.Advance(time.Minute)
			if os, _ := r.MaterializeDue(bgc, 10); len(os) != 1 || os[0].State != tasks.OccReady {
				t.Fatalf("T0 = %+v", os)
			}
			return tmpl.ID
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			h := newSchedHarness(t, newMockProvider(simpleResponseHandler("on it")), "2026-09-30T08:00:00Z")
			h.start()
			sid := h.savedSession()
			h.stop()
			r := h.repo()
			parent := setup(t, h, r, sid)
			before := oneRun(t, r, parent)
			h.clock.Advance(12 * time.Hour)
			h.start()
			h.pass()
			o := assignedAndDelivered(t, h, r, before.ID)
			if o.ConfirmedAt != before.ConfirmedAt || o.Reason == tasks.ReasonRegated {
				t.Fatalf("authorized run gated again: %+v", o)
			}
			oneRun(t, r, parent)
		})
	}
}

// BRIEF Q1: a run on time before the crash but never admitted, found ten
// minutes or more after its due time by the restart, asks again (late=ask):
// nothing reaches the session until Run, then exactly one delivery.
func TestScheduledRestartRegatesNeverAdmitted(t *testing.T) {
	for _, stage := range []string{tasks.OccReady, tasks.OccAssigned} {
		t.Run(stage, func(t *testing.T) {
			prov := newMockProvider(simpleResponseHandler("on it"))
			h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
			h.start()
			sid := h.savedSession()
			h.stop()
			r := h.repo()
			tmpl := mkTemplate(t, r, "nightly", onceAt(h.clock.Now().Add(time.Minute), toSession(sid), tasks.Delivery{}))
			h.clock.Advance(time.Minute)
			os, _ := r.MaterializeDue(bgc, 10)
			if len(os) != 1 || os[0].State != tasks.OccReady {
				t.Fatalf("T0 = %+v", os)
			}
			var oldNotice string
			var oldChild int64
			if stage == tasks.OccAssigned {
				o, err := r.AssignOccurrence(bgc, os[0].ID, tasks.Destination{SessionID: sid})
				if err != nil {
					t.Fatal(err)
				}
				oldNotice, oldChild = o.NoticeID, o.ChildTaskID
			}
			h.clock.Advance(12 * time.Minute)

			h.start()
			h.pass()
			o := oneRun(t, r, tmpl.ID)
			if o.State != tasks.OccLate || o.Reason != tasks.ReasonRegated || o.ChildTaskID != 0 || o.NoticeID != "" {
				t.Fatalf("after restart = %+v", o)
			}
			if oldNotice != "" {
				if n := noticeByID(t, r, oldNotice); n.State != tasks.NoticeFailed || n.Reason != tasks.ReasonRegated {
					t.Fatalf("old notice = %+v", n)
				}
				if _, err := r.Get(bgc, oldChild); !errors.Is(err, tasks.ErrNotFound) {
					t.Fatalf("undelivered child kept: %v", err)
				}
			}
			h.pass()
			if _, live := h.mgr.Get(sid); live || prov.calls.Load() != 0 {
				t.Fatalf("regated run reached its session (live=%v calls=%d)", live, prov.calls.Load())
			}
			if _, err := r.ConfirmOccurrence(bgc, o.ID, o.Revision, tasks.LateRun); err != nil {
				t.Fatal(err)
			}
			h.mgr.planner.nudge()
			h.pass()
			got := assignedAndDelivered(t, h, r, o.ID)
			if got.NoticeID == oldNotice || got.ChildTaskID == oldChild {
				t.Fatalf("confirmed run reused withdrawn links: %+v", got)
			}
			if n := sqlCount(t, h.dbPath(), "SELECT COUNT(*) FROM tasks WHERE place = 'agent'"); n != 1 {
				t.Fatalf("%d children", n)
			}
			if n := sqlCount(t, h.dbPath(), "SELECT COUNT(*) FROM task_notifications WHERE state = 'delivered'"); n != 1 {
				t.Fatalf("%d delivered notices", n)
			}
		})
	}
}

// Another process (the CLI) creating or moving a schedule wakes the planner
// through the existing Watch, even when the database did not exist when serve
// started; the timer follows the new instant.
func TestTaskSchedulerExternalWriterWake(t *testing.T) {
	h := newSchedHarness(t, newMockProvider(simpleResponseHandler("on it")), "2026-09-30T08:00:00Z")
	h.start()
	sess := h.session()
	revs, unsub := h.mgr.taskHub.subscribe()
	defer unsub()
	r := h.repo()
	rec, err := r.AgentSchedule(bgc, tasks.Actor{SessionID: sess.ID}, tasks.AgentInput{Title: "ping"},
		tasks.When{Kind: tasks.WhenOnce, At: h.clock.Now().Add(time.Hour).UnixMilli()}, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-revs:
	case <-time.After(10 * time.Second):
		t.Fatal("no tasks invalidation for the external write")
	}
	h.clock.drainArmed()
	cur, err := r.Get(bgc, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	due := h.clock.Now().Add(10 * time.Second)
	def := onceAt(due, toSession(sess.ID), tasks.Delivery{})
	def.CreatedBySessionID = sess.ID
	if _, err := r.Update(bgc, cur.ID, cur.Revision, tasks.Patch{Schedule: def}); err != nil {
		t.Fatal(err)
	}
	h.clock.waitArmed(t, 10*time.Second)
	h.clock.Advance(10 * time.Second)
	var o tasks.Occurrence
	pollUntil(t, 10*time.Second, "timer fire", func() bool {
		os := runsOfT(t, r, rec.ID)
		if len(os) == 1 {
			o = os[0]
		}
		return len(os) == 1
	})
	if o.DueAt != due.UnixMilli() {
		t.Fatalf("fired at %d, want %d", o.DueAt, due.UnixMilli())
	}
	assignedAndDelivered(t, h, r, o.ID)
}

// A wall-clock jump forward catches up once (coalesced); a jump back never
// fires early, repeats a slot or spins.
func TestTaskSchedulerClockJump(t *testing.T) {
	h := newSchedHarness(t, newMockProvider(), "2026-09-30T08:00:00Z")
	h.start()
	sid := h.session().ID
	r := h.repo()
	tmpl := mkTemplate(t, r, "daily", dailyAt(9, 0, "UTC", toSession(sid), tasks.Delivery{}))
	h.pass()
	h.clock.Set(mustUTC("2026-10-03T09:20:00Z"))
	pollUntil(t, 10*time.Second, "catch-up", func() bool { return len(runsOfT(t, r, tmpl.ID)) == 1 })
	h.pass()
	o := oneRun(t, r, tmpl.ID)
	if o.State != tasks.OccLate || o.MissedCount != 3 || o.DueAt != mustUTC("2026-10-03T09:00:00Z").UnixMilli() {
		t.Fatalf("catch-up = %+v", o)
	}
	cur, _ := r.Get(bgc, tmpl.ID)
	if cur.Next != mustUTC("2026-10-04T09:00:00Z").UnixMilli() {
		t.Fatalf("next = %v", time.UnixMilli(cur.Next).UTC())
	}
	h.clock.Set(mustUTC("2026-10-03T08:00:00Z"))
	h.clock.drainArmed()
	h.pass()
	h.clock.waitArmed(t, 30*time.Second)
	if len(runsOfT(t, r, tmpl.ID)) != 1 {
		t.Fatal("a backward jump fired a slot")
	}
	h.clock.Set(mustUTC("2026-10-04T09:00:00Z"))
	pollUntil(t, 10*time.Second, "next slot", func() bool { return len(runsOfT(t, r, tmpl.ID)) == 2 })
	h.pass()
	// The unanswered late run is coalesced into the next slot's run, which
	// stands for it and for the three it already stood for.
	if os := runsOfT(t, r, tmpl.ID); len(os) != 2 || os[1].State == tasks.OccLate || os[1].MissedCount != 4 ||
		os[0].State != tasks.OccSkipped || os[0].Reason != tasks.ReasonSuperseded {
		t.Fatalf("runs = %+v", os)
	}
}

// Repository gestures and the planner never wait for a session's lifecycle
// lock, even while the dispatcher is blocked on it behind a queued writer.
func TestTaskSchedulerLifecycleLockOrder(t *testing.T) {
	admitting := make(chan string, 4)
	h := newSchedHarness(t, newMockProvider(simpleResponseHandler("ok")), "2026-09-30T08:00:00Z")
	h.hooks.beforeAdmit = func(id string) {
		select {
		case admitting <- id:
		default:
		}
	}
	h.start()
	sess := h.session()
	r := h.repo()
	mkTemplate(t, r, "first", onceAt(h.clock.Now().Add(time.Minute), toSession(sess.ID), tasks.Delivery{}))

	sess.lifecycle.RLock() // a command inside its read section
	writer := make(chan struct{})
	go func() { sess.lifecycle.Lock(); sess.lifecycle.Unlock(); close(writer) }() //nolint:staticcheck // barrier
	pollUntil(t, 5*time.Second, "writer queued", func() bool {
		if sess.lifecycle.TryRLock() {
			sess.lifecycle.RUnlock()
			return false
		}
		return true
	})
	h.clock.Advance(time.Minute)
	h.mgr.planner.nudge()
	select {
	case <-admitting:
	case <-time.After(10 * time.Second):
		t.Fatal("dispatcher never reached admission")
	}
	gestures := make(chan error, 1)
	go func() {
		second := mkTemplate(t, r, "second", onceAt(h.clock.Now().Add(time.Hour), toSession(sess.ID), tasks.Delivery{}))
		cur, err := h.mgr.tasks.Get(bgc, second.ID)
		if err == nil {
			_, err = h.mgr.tasks.Update(bgc, cur.ID, cur.Revision, tasks.Patch{Title: ptrStr("second, edited")})
		}
		if err == nil {
			h.mgr.planner.nudge()
			h.pass()
		}
		gestures <- err
	}()
	select {
	case err := <-gestures:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("task gestures or the planner wait behind the lifecycle lock")
	}
	sess.lifecycle.RUnlock()
	select {
	case <-writer:
	case <-time.After(10 * time.Second):
		t.Fatal("lifecycle writer never finished")
	}
}

func ptrStr(s string) *string { return &s }

// Shutdown waits for the planner to leave a provisioning step before any
// session is torn down; the interrupted run recovers in a fresh Manager.
func TestTaskSchedulerShutdownJoinsWorkers(t *testing.T) {
	entered := make(chan int64, 1)
	release := make(chan struct{})
	prov := newMockProvider(simpleResponseHandler("ok"))
	h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
	h.start()
	sid := h.savedSession()
	h.stop()
	r := h.repo()
	tmpl := mkTemplate(t, r, "job", onceAt(h.clock.Now().Add(time.Minute), toSession(sid), tasks.Delivery{}))
	h.clock.Advance(time.Minute)
	h.hooks.beforeAssign = func(id int64) {
		entered <- id
		<-release
	}
	m := h.start()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("planner never provisioned")
	}
	down := make(chan struct{})
	go func() { m.Shutdown(); close(down) }()
	select {
	case <-down:
		t.Fatal("Shutdown returned while the planner was provisioning")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case <-down:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown never returned")
	}
	h.mgr = nil
	o := oneRun(t, r, tmpl.ID)
	if o.AdmittedAt != 0 || prov.calls.Load() != 0 {
		t.Fatalf("delivered during shutdown: %+v calls=%d", o, prov.calls.Load())
	}
	h.hooks.beforeAssign = nil
	h.start()
	h.pass()
	assignedAndDelivered(t, h, r, o.ID)
}
