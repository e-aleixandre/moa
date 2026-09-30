package serve

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// Regression tests for the adversarial review of 43c0d909: real Manager and
// SQLite, a controlled clock and provider, and SIGKILLed child processes.

const schedReviewChildEnv = "MOA_SCHED_REVIEW_CHILD"

func schedReviewStopWorkers(t *testing.T, m *Manager) {
	t.Helper()
	m.tasksCancel()
	for _, done := range []<-chan struct{}{m.notices.done, m.planner.done} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("test setup: task worker did not stop")
		}
	}
}

func schedReviewKill() {
	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		panic(err)
	}
	select {}
}

// TestScheduleReviewCrashChild is the process schedReviewCrash kills at a
// boundary: it uses the real Manager and SQLite and dies without Shutdown.
func TestScheduleReviewCrashChild(t *testing.T) {
	dir := os.Getenv(schedReviewChildEnv)
	if dir == "" {
		t.Skip("SIGKILL helper")
	}
	h := newSchedHarnessIn(t, dir, newMockProvider(simpleResponseHandler("ok")), os.Getenv("MOA_SCHED_REVIEW_AT"))
	switch boundary := os.Getenv("MOA_SCHED_REVIEW_BOUNDARY"); boundary {
	case "after_t0":
		h.hooks.beforeAssign = func(int64) { schedReviewKill() }
	case "reserved_before_admit":
		h.hooks.beforeAdmit = func(string) { schedReviewKill() }
	default:
		t.Fatalf("test setup: unknown crash boundary %q", boundary)
	}
	h.start()
	h.pass()
	// Delivery runs in the notice goroutine; give it time to hit the hook.
	time.Sleep(8 * time.Second)
	t.Fatal("test setup: child missed the crash boundary")
}

func schedReviewCrash(t *testing.T, h *schedHarness, boundary string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestScheduleReviewCrashChild$", "-test.count=1", "-test.timeout=15s", "-test.v")
	cmd.Env = append(os.Environ(), schedReviewChildEnv+"="+h.dir,
		"MOA_SCHED_REVIEW_AT="+h.clock.Now().Format(time.RFC3339Nano),
		"MOA_SCHED_REVIEW_BOUNDARY="+boundary)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("test setup: child did not die: %v\n%s", err, out)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("test setup: child was not SIGKILLed at %s: %v\n%s", boundary, err, out)
	}
}

// Q1: a crash after the assignment was reserved (sent) but before it was
// admitted leaves no trace in the transcript. At a restart ten minutes late
// with late=ask, the run waits for the owner's OK instead of running.
func TestScheduleReviewReservedNotAdmittedIsRegated(t *testing.T) {
	prov := newMockProvider(simpleResponseHandler("ran without owner OK"))
	h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
	h.start()
	sid := h.savedSession()
	h.stop()
	r := h.repo()
	tmpl := mkTemplate(t, r, "late after reservation crash", onceAt(h.clock.Now().Add(time.Minute), toSession(sid), tasks.Delivery{Saved: tasks.DeliverWake}))
	h.clock.Advance(time.Minute)
	due, err := r.MaterializeDue(bgc, 10)
	if err != nil || len(due) != 1 || due[0].State != tasks.OccReady {
		t.Fatalf("test setup: T0 = %+v / %v", due, err)
	}
	schedReviewCrash(t, h, "reserved_before_admit")
	before := oneRun(t, r, tmpl.ID)
	n := noticeByID(t, r, before.NoticeID)
	if before.State != tasks.OccAssigned || before.AdmittedAt != 0 || n.State != tasks.NoticeSent ||
		transcriptNoticeCount(t, h.base, sid, n.ID) != 0 {
		t.Fatalf("test setup: not a never-admitted reservation: run %+v notice %+v", before, n)
	}
	h.clock.Advance(12 * time.Minute)
	h.start()
	h.pass()
	after := occNow(t, r, before.ID)
	if after.State != tasks.OccLate {
		t.Fatalf("Q1: never-admitted run after restart = %s/%s (provider calls %d), want late", after.State, after.Reason, prov.calls.Load())
	}
	if got := noticeByID(t, r, n.ID); got.State != tasks.NoticeFailed {
		t.Errorf("withdrawn assignment = %s/%s, want failed", got.State, got.Reason)
	}
	if got := transcriptNoticeCount(t, h.base, sid, n.ID); got != 0 || prov.calls.Load() != 0 {
		t.Errorf("assignment delivered %d time(s), %d provider calls; want none", got, prov.calls.Load())
	}
}

// Q1 control: a reservation whose notice did reach the transcript is
// delivered, not re-gated, however late the restart.
func TestScheduleReviewReservedAndSavedIsNotRegated(t *testing.T) {
	h := newSchedHarness(t, newMockProvider(simpleResponseHandler("ok")), "2026-09-30T08:00:00Z")
	h.start()
	sid := h.savedSession()
	h.stop()
	r := h.repo()
	tmpl := mkTemplate(t, r, "saved before crash", onceAt(h.clock.Now().Add(time.Minute), toSession(sid), tasks.Delivery{Saved: tasks.DeliverHold}))
	h.clock.Advance(time.Minute)
	due, err := r.MaterializeDue(bgc, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("test setup: T0 = %+v / %v", due, err)
	}
	o, err := r.AssignOccurrence(bgc, due[0].ID, tasks.Destination{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	// The assignment reached the saved transcript, then the process died
	// before recording it: the notice is still sent.
	h.start()
	sess, err := h.mgr.ResumeSession(sid)
	if err != nil {
		t.Fatal(err)
	}
	n := waitNoticeByID(t, r, o.NoticeID, tasks.NoticeDelivered)
	pollUntil(t, 10*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })
	h.stop()
	sqlExec(t, h.dbPath(), "UPDATE task_notifications SET state = 'sent' WHERE id = ?", n.ID)
	sqlExec(t, h.dbPath(), "UPDATE task_occurrences SET admitted_at = NULL WHERE id = ?", o.ID)
	h.clock.Advance(20 * time.Minute)
	h.start()
	got := waitOcc(t, r, o.ID, "admitted", func(o tasks.Occurrence) bool { return o.AdmittedAt != 0 })
	if got.State != tasks.OccAssigned || noticeByID(t, r, n.ID).State != tasks.NoticeDelivered || oneRun(t, r, tmpl.ID).ID != o.ID {
		t.Fatalf("saved assignment was re-gated: %+v", got)
	}
}

// Q2: a recurring run materialized before a crash and never assigned is
// coalesced with the runs missed during the downtime into the latest one.
func TestScheduleReviewRecurringCrashCoalesces(t *testing.T) {
	for _, late := range []string{tasks.LateAsk, tasks.LateRun} {
		t.Run(late, func(t *testing.T) {
			prov := newMockProvider(simpleResponseHandler("newest run"), simpleResponseHandler("older run"))
			h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
			h.start()
			sid := h.savedSession()
			h.stop()
			r := h.repo()
			tmpl := mkTemplate(t, r, "daily interrupted before T1", dailyAt(9, 0, "UTC", toSession(sid), tasks.Delivery{Late: late}))
			h.clock.Set(mustUTC("2026-09-30T09:00:00Z"))
			due, err := r.MaterializeDue(bgc, 10)
			if err != nil || len(due) != 1 || due[0].State != tasks.OccReady {
				t.Fatalf("test setup: T0 = %+v / %v", due, err)
			}
			schedReviewCrash(t, h, "after_t0")
			if old := occNow(t, r, due[0].ID); old.State != tasks.OccReady || old.ChildTaskID != 0 {
				t.Fatalf("test setup: SIGKILL did not preserve an unassigned run: %+v", old)
			}
			h.clock.Set(mustUTC("2026-10-03T09:01:00Z"))
			h.start()
			h.pass()
			runs := runsOfT(t, r, tmpl.ID)
			if len(runs) != 2 {
				t.Fatalf("test setup: expected stored original and latest slot, got %+v", runs)
			}
			latest := assignedAndDelivered(t, h, r, runs[1].ID)
			old := occNow(t, r, runs[0].ID)
			delivered := sqlCount(t, h.dbPath(), "SELECT COUNT(*) FROM task_notifications WHERE state = 'delivered'")
			if old.State != tasks.OccSkipped || old.Reason != tasks.ReasonSuperseded || latest.MissedCount != 3 || delivered != 1 {
				t.Errorf("Q2: old run %s/%s, latest missed_count=%d, delivered notices=%d; want superseded, 3, 1",
					old.State, old.Reason, latest.MissedCount, delivered)
			}
		})
	}
}

// A Delete refused because a Resume reserved the session in the meantime
// settles nothing: the session lives on with its scheduled work intact.
// Either Delete wins (the file is gone) or it is refused untouched.
func TestScheduleReviewDeleteRacingResumeSettlesOnlyWhenDeleted(t *testing.T) {
	prov := newMockProvider()
	h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
	m := h.start()
	schedReviewStopWorkers(t, m)
	sid := h.savedSession()
	r := h.repo()
	tmpl := mkTemplate(t, r, "held assignment", onceAt(h.clock.Now().Add(time.Minute), toSession(sid), tasks.Delivery{Saved: tasks.DeliverHold}))
	h.clock.Advance(time.Minute)
	due, err := r.MaterializeDue(bgc, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("test setup: T0 = %+v / %v", due, err)
	}
	o, err := r.AssignOccurrence(bgc, due[0].ID, tasks.Destination{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	settling, resumeStarted := make(chan struct{}), make(chan struct{})
	releaseSettle, releaseResume := make(chan struct{}), make(chan struct{})
	var settleOnce, startedOnce, releaseSettleOnce, releaseResumeOnce sync.Once
	unblockSettle := func() { releaseSettleOnce.Do(func() { close(releaseSettle) }) }
	unblockResume := func() { releaseResumeOnce.Do(func() { close(releaseResume) }) }
	defer unblockSettle()
	defer unblockResume()
	m.tasks.SetClock(func() time.Time {
		settleOnce.Do(func() {
			close(settling)
			<-releaseSettle
		})
		return h.clock.Now()
	})
	m.providerFactory = func(core.Model) (core.Provider, error) {
		startedOnce.Do(func() { close(resumeStarted) })
		<-releaseResume
		return prov, nil
	}
	deleted := make(chan error, 1)
	go func() { deleted <- m.Delete(sid) }()
	select {
	case <-settling:
	case <-time.After(5 * time.Second):
		t.Fatal("test setup: Delete did not reach its settlement transaction")
	}
	resumed := make(chan error, 1)
	go func() {
		_, err := m.ResumeSession(sid)
		resumed <- err
	}()
	// Give Resume the chance to reserve the session while Delete settles.
	select {
	case <-resumeStarted:
	case <-time.After(time.Second):
	}
	unblockSettle()
	var deleteErr error
	select {
	case deleteErr = <-deleted:
	case <-time.After(5 * time.Second):
		t.Fatal("test setup: Delete did not return after settlement")
	}
	unblockResume()
	var resumeErr error
	select {
	case resumeErr = <-resumed:
	case <-time.After(5 * time.Second):
		t.Fatal("test setup: Resume did not finish")
	}
	m.tasks.SetClock(h.clock.Now)
	after := oneRun(t, r, tmpl.ID)
	n := noticeByID(t, r, o.NoticeID)
	_, _, findErr := session.FindSessionReadOnly(h.base, sid)
	t.Logf("delete=%v resume=%v file=%v; run %s/%s notice %s/%s", deleteErr, resumeErr, findErr, after.State, after.Reason, n.State, n.Reason)
	switch {
	case errors.Is(deleteErr, ErrBusy):
		if resumeErr != nil || findErr != nil {
			t.Fatalf("refused Delete: resume=%v find=%v", resumeErr, findErr)
		}
		if after.State != tasks.OccAssigned || n.State != tasks.NoticeHeld {
			t.Errorf("refused Delete settled live work: run=%s/%s notice=%s/%s", after.State, after.Reason, n.State, n.Reason)
		}
	case deleteErr == nil:
		if !errors.Is(findErr, session.ErrNotFound) || resumeErr == nil {
			t.Fatalf("Delete succeeded but the session survived: resume=%v find=%v", resumeErr, findErr)
		}
		if after.State != tasks.OccFailed || after.Reason != tasks.ReasonSessionDeleted {
			t.Errorf("deleted session's run = %s/%s, want failed/%s", after.State, after.Reason, tasks.ReasonSessionDeleted)
		}
	default:
		t.Fatalf("Delete = %v", deleteErr)
	}
}
