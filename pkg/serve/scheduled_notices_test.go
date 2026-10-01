package serve

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/bus"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/owner"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// fireOnce makes a once template for target due in a minute, crosses the
// due time and runs a planner pass: the run is assigned (T1).
func fireOnce(t *testing.T, h *schedHarness, r *tasks.Repo, title string, target tasks.Target, d tasks.Delivery) tasks.Occurrence {
	t.Helper()
	tmpl := mkTemplate(t, r, title, onceAt(h.clock.Now().Add(time.Minute), target, d))
	h.clock.Advance(time.Minute)
	h.pass()
	o := oneRun(t, r, tmpl.ID)
	if o.NoticeID == "" || o.ChildTaskID == 0 {
		t.Fatalf("run after firing = %+v", o)
	}
	return o
}

func startRunning(t *testing.T, h *schedHarness, sess *ManagedSession, started <-chan struct{}) {
	t.Helper()
	if _, _, _, err := h.mgr.Send(sess.ID, "work", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("run never started")
	}
}

func steerQueued(sess *ManagedSession, id string) bool {
	steers, _ := bus.QueryTyped[bus.GetPendingSteers, []core.SteerItem](sess.runtime.Bus, bus.GetPendingSteers{})
	for _, s := range steers {
		if s.Custom["id"] == id {
			return true
		}
	}
	return false
}

// busy=wait never steers: the run's assignment waits until the session is
// really free, then starts a fresh turn. A competing run that slips in
// between the dispatcher's look and admission is refused by the atomic
// IdleOnly gate, and the 30s tick retries a wait no idle event announces.
func TestScheduledNoticeBusyWaitAdmission(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		started, release := make(chan struct{}, 1), make(chan struct{})
		prov := newMockProvider(blockingHandler(started, release, "busy"), simpleResponseHandler("did it"))
		h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
		h.start()
		sess := h.session()
		startRunning(t, h, sess, started)
		r := h.repo()
		o := fireOnce(t, h, r, "wait for me", toSession(sess.ID), tasks.Delivery{Busy: tasks.BusyWait})
		pollUntil(t, 10*time.Second, "waiting for idle", func() bool {
			n := noticeByID(t, r, o.NoticeID)
			return n.State == tasks.NoticePending && n.Reason == noticeReasonBusyWait
		})
		if steerQueued(sess, o.NoticeID) || len(noticeMessages(sess.History(), o.NoticeID)) != 0 {
			t.Fatal("busy=wait steered into the working run")
		}
		close(release)
		waitNoticeByID(t, r, o.NoticeID, tasks.NoticeDelivered)
		msgs := noticeMessages(sess.History(), o.NoticeID)
		if len(msgs) != 1 || msgs[0].Custom["steer"] != nil || msgs[0].Custom["autorun"] != true {
			t.Fatalf("waited assignment = %+v", msgs)
		}
		pollUntil(t, 5*time.Second, "fresh turn", func() bool { return hasAssistantAfter(sess.History(), o.NoticeID) })
	})
	t.Run("race", func(t *testing.T) {
		started, release := make(chan struct{}, 1), make(chan struct{})
		prov := newMockProvider(blockingHandler(started, release, "competing"), simpleResponseHandler("did it"))
		h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
		var sess *ManagedSession
		var raced atomic.Bool
		h.hooks.beforeAdmit = func(string) {
			if raced.Swap(true) {
				return
			}
			if _, _, _, err := h.mgr.Send(sess.ID, "competing", nil, "", ""); err != nil {
				t.Error(err)
			}
			<-started
		}
		h.start()
		sess = h.session()
		r := h.repo()
		o := fireOnce(t, h, r, "wait for me", toSession(sess.ID), tasks.Delivery{Busy: tasks.BusyWait})
		pollUntil(t, 10*time.Second, "refused by the gate", func() bool {
			n := noticeByID(t, r, o.NoticeID)
			return raced.Load() && n.State == tasks.NoticePending && n.Reason == noticeReasonBusyWait
		})
		if steerQueued(sess, o.NoticeID) {
			t.Fatal("IdleOnly assignment queued as a steer")
		}
		close(release)
		waitNoticeByID(t, r, o.NoticeID, tasks.NoticeDelivered)
		if msgs := noticeMessages(sess.History(), o.NoticeID); len(msgs) != 1 || msgs[0].Custom["steer"] != nil {
			t.Fatalf("assignment = %+v", msgs)
		}
	})
	t.Run("tick", func(t *testing.T) {
		started, release := make(chan struct{}, 1), make(chan struct{})
		prov := newMockProvider(blockingHandler(started, release, "busy"), simpleResponseHandler("did it"))
		h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
		h.start()
		sess := h.session()
		startRunning(t, h, sess, started)
		r := h.repo()
		// Due in 1ms, so crossing it does not also fire the 30s tick.
		tmpl := mkTemplate(t, r, "quiet", onceAt(h.clock.Now().Add(time.Millisecond), toSession(sess.ID), tasks.Delivery{Busy: tasks.BusyWait}))
		h.clock.Advance(time.Millisecond)
		h.pass()
		o := oneRun(t, r, tmpl.ID)
		pollUntil(t, 10*time.Second, "waiting for idle", func() bool {
			return noticeByID(t, r, o.NoticeID).Reason == noticeReasonBusyWait
		})
		// The session becomes free without the idle event reaching the
		// notice (background work ending quietly).
		d := h.mgr.notices
		d.trigMu.Lock()
		delete(d.waitingIdle, sess.ID)
		d.trigMu.Unlock()
		close(release)
		pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })
		d.pass(bgc)
		if n := noticeByID(t, r, o.NoticeID); n.State != tasks.NoticePending {
			t.Fatalf("waiting notice retried without a trigger: %+v", n)
		}
		h.clock.Advance(noticeReconcileInterval)
		waitNoticeByID(t, r, o.NoticeID, tasks.NoticeDelivered)
		oneRun(t, r, tmpl.ID)
	})
}

// Waiting on purpose (hold, busy) while the server runs is never lateness:
// no confirmation is asked however long it takes.
func TestScheduledNoticeWaitDoesNotBecomeLate(t *testing.T) {
	t.Run("hold", func(t *testing.T) {
		h := newSchedHarness(t, newMockProvider(simpleResponseHandler("ok")), "2026-09-30T08:00:00Z")
		h.start()
		sid := h.savedSession()
		r := h.repo()
		o := fireOnce(t, h, r, "held", toSession(sid), tasks.Delivery{Saved: tasks.DeliverHold})
		waitNoticeByID(t, r, o.NoticeID, tasks.NoticeHeld)
		h.clock.Advance(3 * time.Hour)
		h.pass()
		if got := occNow(t, r, o.ID); got.State != tasks.OccAssigned || got.Reason != "" {
			t.Fatalf("held run = %+v", got)
		}
		if _, err := h.mgr.ResumeSession(sid); err != nil {
			t.Fatal(err)
		}
		assignedAndDelivered(t, h, r, o.ID)
	})
	t.Run("busy", func(t *testing.T) {
		started, release := make(chan struct{}, 1), make(chan struct{})
		h := newSchedHarness(t, newMockProvider(blockingHandler(started, release, "busy"), simpleResponseHandler("ok")), "2026-09-30T08:00:00Z")
		h.start()
		sess := h.session()
		startRunning(t, h, sess, started)
		r := h.repo()
		o := fireOnce(t, h, r, "later", toSession(sess.ID), tasks.Delivery{Busy: tasks.BusyWait})
		pollUntil(t, 10*time.Second, "waiting", func() bool { return noticeByID(t, r, o.NoticeID).Reason == noticeReasonBusyWait })
		h.clock.Advance(3 * time.Hour)
		h.pass()
		if got := occNow(t, r, o.ID); got.State != tasks.OccAssigned {
			t.Fatalf("waiting run = %+v", got)
		}
		close(release)
		waitNoticeByID(t, r, o.NoticeID, tasks.NoticeDelivered)
	})
}

// The default (steer) joins a working run as one steer; there is one child
// and one notice, delivered through the ordinary dispatcher.
func TestScheduledNoticeSteersDefault(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	prov := newMockProvider(blockingHandler(started, release, "busy"), simpleResponseHandler("read it"))
	h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
	h.start()
	sess := h.session()
	startRunning(t, h, sess, started)
	r := h.repo()
	o := fireOnce(t, h, r, "steer me", toSession(sess.ID), tasks.Delivery{})
	pollUntil(t, 10*time.Second, "steer queued", func() bool { return steerQueued(sess, o.NoticeID) })
	close(release)
	waitNoticeByID(t, r, o.NoticeID, tasks.NoticeDelivered)
	msgs := noticeMessages(sess.History(), o.NoticeID)
	if len(msgs) != 1 || msgs[0].Custom["steer"] != true || msgs[0].Custom["source_name"] != "tasks" {
		t.Fatalf("steered assignment = %+v", msgs)
	}
	if n := sqlCount(t, h.dbPath(), "SELECT COUNT(*) FROM task_notifications"); n != 1 {
		t.Fatalf("%d notices", n)
	}
	if got := occNow(t, r, o.ID); got.AdmittedAt == 0 {
		t.Fatalf("admission not recorded: %+v", got)
	}
}

// A saved session: hold waits for the owner to open it (also across a
// restart); wake resumes it and runs once.
func TestScheduledNoticeSavedHoldAndWake(t *testing.T) {
	t.Run("hold", func(t *testing.T) {
		prov := newMockProvider(simpleResponseHandler("ok"))
		h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
		h.start()
		sid := h.savedSession()
		r := h.repo()
		o := fireOnce(t, h, r, "held", toSession(sid), tasks.Delivery{Saved: tasks.DeliverHold})
		waitNoticeByID(t, r, o.NoticeID, tasks.NoticeHeld)
		h.stop()
		h.clock.Advance(2 * time.Minute)
		h.start()
		h.pass()
		h.mgr.notices.nudge()
		if _, live := h.mgr.Get(sid); live || prov.calls.Load() != 0 {
			t.Fatal("hold resumed the session")
		}
		if n := noticeByID(t, r, o.NoticeID); n.State != tasks.NoticeHeld {
			t.Fatalf("after restart = %+v", n)
		}
		if _, err := h.mgr.ResumeSession(sid); err != nil {
			t.Fatal(err)
		}
		assignedAndDelivered(t, h, r, o.ID)
	})
	t.Run("wake", func(t *testing.T) {
		prov := newMockProvider(simpleResponseHandler("ok"))
		h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
		h.start()
		sid := h.savedSession()
		r := h.repo()
		o := fireOnce(t, h, r, "wake", toSession(sid), tasks.Delivery{Saved: tasks.DeliverWake})
		assignedAndDelivered(t, h, r, o.ID)
		pollUntil(t, 5*time.Second, "turn", func() bool {
			s, ok := h.mgr.Get(sid)
			return ok && hasAssistantAfter(s.History(), o.NoticeID)
		})
	})
	t.Run("wake_after_restart", func(t *testing.T) {
		h := newSchedHarness(t, newMockProvider(simpleResponseHandler("ok")), "2026-09-30T08:00:00Z")
		h.start()
		sid := h.savedSession()
		h.stop()
		r := h.repo()
		tmpl := mkTemplate(t, r, "wake", onceAt(h.clock.Now().Add(time.Minute), toSession(sid), tasks.Delivery{Saved: tasks.DeliverWake}))
		h.clock.Advance(time.Minute)
		os, _ := r.MaterializeDue(bgc, 10)
		if _, err := r.AssignOccurrence(bgc, os[0].ID, tasks.Destination{SessionID: sid}); err != nil {
			t.Fatal(err)
		}
		h.start()
		assignedAndDelivered(t, h, r, oneRun(t, r, tmpl.ID).ID)
	})
}

// A scheduled assignment that cannot be delivered fails as Not sent: its
// notice and its run together, in one commit. An aborted commit settles
// nothing, and a failure never mints another assignment.
func TestScheduledNoticeHardFailureAtomic(t *testing.T) {
	check := func(t *testing.T, r *tasks.Repo, o tasks.Occurrence, reason string) {
		t.Helper()
		n := waitNoticeByID(t, r, o.NoticeID, tasks.NoticeFailed)
		got := occNow(t, r, o.ID)
		if n.Reason != reason || got.State != tasks.OccFailed || got.Reason != reason {
			t.Fatalf("notice %+v run %+v, want failed/%s", n, got, reason)
		}
	}
	t.Run("deleted_recipient", func(t *testing.T) {
		h := newSchedHarness(t, newMockProvider(), "2026-09-30T08:00:00Z")
		h.start()
		sid := h.savedSession()
		h.stop()
		r := h.repo()
		tmpl := mkTemplate(t, r, "gone", onceAt(h.clock.Now().Add(time.Minute), toSession(sid), tasks.Delivery{}))
		h.clock.Advance(time.Minute)
		os, _ := r.MaterializeDue(bgc, 10)
		o, err := r.AssignOccurrence(bgc, os[0].ID, tasks.Destination{SessionID: sid})
		if err != nil {
			t.Fatal(err)
		}
		if err := session.DeleteByID(h.base, sid); err != nil {
			t.Fatal(err)
		}
		h.start()
		check(t, r, o, tasks.ReasonSessionDeleted)
		h.pass()
		oneRun(t, r, tmpl.ID)
		if n := sqlCount(t, h.dbPath(), "SELECT COUNT(*) FROM task_notifications"); n != 1 {
			t.Fatalf("%d notices after failure", n)
		}
	})
	t.Run("resume_failed", func(t *testing.T) {
		h := newSchedHarness(t, newMockProvider(), "2026-09-30T08:00:00Z")
		h.start()
		sid := h.savedSession()
		h.factoryErr.Store(true)
		r := h.repo()
		ensureDB(t, r)
		// The SQL of the terminal transition is refused once: nothing settles.
		drop := abortTrigger(t, h.dbPath(), "no_fail", "BEFORE UPDATE OF state ON task_occurrences WHEN NEW.state = 'failed'")
		o := fireOnce(t, h, r, "unresumable", toSession(sid), tasks.Delivery{})
		pollUntil(t, 10*time.Second, "an attempt", func() bool { return h.attempts.Load() > 0 })
		h.mgr.notices.nudge()
		if n := noticeByID(t, r, o.NoticeID); n.State == tasks.NoticeFailed {
			t.Fatalf("aborted commit settled the notice: %+v", n)
		}
		if got := occNow(t, r, o.ID); got.State != tasks.OccAssigned {
			t.Fatalf("aborted commit settled the run: %+v", got)
		}
		drop()
		h.mgr.notices.trigger(func() { h.mgr.notices.retry[sid] = true })
		check(t, r, o, reasonResumeFailed)
	})
	t.Run("session_limit", func(t *testing.T) {
		prev := maxNoticeLoadedSessions
		maxNoticeLoadedSessions = 1
		t.Cleanup(func() { maxNoticeLoadedSessions = prev })
		h := newSchedHarness(t, newMockProvider(), "2026-09-30T08:00:00Z")
		h.start()
		sid := h.savedSession()
		h.session() // occupies the only resident slot
		r := h.repo()
		o := fireOnce(t, h, r, "capped", toSession(sid), tasks.Delivery{})
		check(t, r, o, tasks.ReasonSessionLimit)
	})
	t.Run("steer_queue_full", func(t *testing.T) {
		started, release := make(chan struct{}, 1), make(chan struct{})
		defer close(release)
		h := newSchedHarness(t, newMockProvider(blockingHandler(started, release, "busy")), "2026-09-30T08:00:00Z")
		h.start()
		sess := h.session()
		startRunning(t, h, sess, started)
		for i := 0; ; i++ {
			if err := sess.runtime.Bus.Execute(bus.SteerAgent{ID: core.NewSteerID(), Text: fmt.Sprint("fill ", i)}); err != nil {
				break
			}
		}
		r := h.repo()
		o := fireOnce(t, h, r, "no room", toSession(sess.ID), tasks.Delivery{})
		check(t, r, o, reasonSteerQueueFull)
	})
}

// An owner target resolves to the owner's conversation when the run is
// released (after the late OK), and keeps it; a missing owner or one with
// no conversation fails, and no owner is ever created.
func TestScheduledOwnerResolvedAtRelease(t *testing.T) {
	h := newSchedHarness(t, newMockProvider(simpleResponseHandler("ok")), "2026-09-30T08:00:00Z")
	h.start()
	first := h.savedSession()
	second := h.savedSession()
	third := h.savedSession()
	store, err := owner.Default()
	if err != nil {
		t.Fatal(err)
	}
	own, err := store.Create(h.root, "Ops", "", "", false, owner.Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	own.SessionID = first
	if err := store.Save(own); err != nil {
		t.Fatal(err)
	}
	h.stop()
	r := h.repo()
	target := tasks.Target{Kind: tasks.TargetOwner, ID: own.ID}
	tmpl := mkTemplate(t, r, "owner job", onceAt(h.clock.Now().Add(time.Minute), target, tasks.Delivery{}))
	h.clock.Advance(time.Minute + tasks.LateAfter)
	h.start()
	h.pass()
	o := oneRun(t, r, tmpl.ID)
	if o.State != tasks.OccLate {
		t.Fatalf("run = %+v", o)
	}
	own.SessionID = second
	if err := store.Save(own); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ConfirmOccurrence(bgc, o.ID, o.Revision, tasks.LateRun); err != nil {
		t.Fatal(err)
	}
	h.pass()
	got := assignedAndDelivered(t, h, r, o.ID)
	if got.SessionID != second {
		t.Fatalf("resolved to %s, want the owner's current %s", got.SessionID, second)
	}
	own.SessionID = third
	if err := store.Save(own); err != nil {
		t.Fatal(err)
	}
	h.pass()
	if got := occNow(t, r, o.ID); got.SessionID != second {
		t.Fatalf("resolution not frozen: %+v", got)
	}

	for name, tc := range map[string]struct {
		id     string
		reason string
		setup  func()
	}{
		"missing":    {id: "nobody", reason: reasonOwnerMissing},
		"no_session": {id: own.ID, reason: reasonOwnerHasNoSession, setup: func() { own.SessionID = ""; _ = store.Save(own) }},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup()
			}
			before, _ := store.List()
			o := fireOnceExpect(t, h, r, name, tasks.Target{Kind: tasks.TargetOwner, ID: tc.id})
			if o.State != tasks.OccFailed || o.Reason != tc.reason {
				t.Fatalf("run = %+v, want failed/%s", o, tc.reason)
			}
			if after, _ := store.List(); len(after) != len(before) {
				t.Fatal("an owner was created for the run")
			}
		})
	}
}

// fireOnceExpect fires a once template and returns its run whatever became of it.
func fireOnceExpect(t *testing.T, h *schedHarness, r *tasks.Repo, title string, target tasks.Target) tasks.Occurrence {
	t.Helper()
	tmpl := mkTemplate(t, r, title, onceAt(h.clock.Now().Add(time.Minute), target, tasks.Delivery{}))
	h.clock.Advance(time.Minute)
	h.pass()
	return oneRun(t, r, tmpl.ID)
}

// An assignment already saved in the transcript whose row is still pending
// (an older binary) is recognized by its notice ID: no second message or
// run, and the admission time is repaired. A sent row is settled by its
// recorded admission instead (TestRound3AckNotTranscriptDecides).
func TestScheduledNoticePersistedBeforeAcknowledgment(t *testing.T) {
	for _, state := range []string{tasks.NoticePending} {
		t.Run(state, func(t *testing.T) {
			prov := newMockProvider(simpleResponseHandler("ok"))
			h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
			h.start()
			sess := h.session()
			r := h.repo()
			o := fireOnce(t, h, r, "saved", toSession(sess.ID), tasks.Delivery{})
			assignedAndDelivered(t, h, r, o.ID)
			pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })
			closeSession(t, h.mgr, sess.ID)
			h.stop()
			calls := prov.calls.Load()
			sqlExec(t, h.dbPath(), "UPDATE task_notifications SET state = ?, delivered_at = NULL WHERE id = ?", state, o.NoticeID)
			sqlExec(t, h.dbPath(), "UPDATE task_occurrences SET admitted_at = NULL WHERE id = ?", o.ID)
			h.clock.Advance(time.Minute)
			h.start()
			got := waitOcc(t, r, o.ID, "admission repaired", func(o tasks.Occurrence) bool { return o.AdmittedAt != 0 })
			waitNoticeByID(t, r, o.NoticeID, tasks.NoticeDelivered)
			if n := transcriptNoticeCount(t, h.base, sess.ID, o.NoticeID); n != 1 || prov.calls.Load() != calls {
				t.Fatalf("redelivered: %d copies, calls %d→%d", n, calls, prov.calls.Load())
			}
			if got.State != tasks.OccAssigned {
				t.Fatalf("run = %+v", got)
			}
		})
	}
}

// When the pending→sent reservation cannot commit, nothing is admitted.
func TestScheduledNoticeNoAdmissionWithoutCommit(t *testing.T) {
	prov := newMockProvider(simpleResponseHandler("ok"))
	h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
	h.start()
	sess := h.session()
	r := h.repo()
	ensureDB(t, r)
	drop := abortTrigger(t, h.dbPath(), "no_reserve", "BEFORE UPDATE OF state ON task_notifications WHEN NEW.state = 'sent'")
	o := fireOnce(t, h, r, "blocked", toSession(sess.ID), tasks.Delivery{})
	pollUntil(t, 10*time.Second, "an attempt", func() bool { return h.attempts.Load() > 0 })
	h.mgr.notices.nudge()
	if n := noticeByID(t, r, o.NoticeID); n.State != tasks.NoticePending {
		t.Fatalf("notice = %+v", n)
	}
	if prov.calls.Load() != 0 || len(noticeMessages(sess.History(), o.NoticeID)) != 0 {
		t.Fatal("admitted without a committed reservation")
	}
	drop()
	h.mgr.notices.trigger(func() { h.mgr.notices.retryAll = true })
	assignedAndDelivered(t, h, r, o.ID)
}

// The assignment carries the run's identity and times, rendered in the
// template's zone; "after your OK" only for a confirmed late run.
func TestScheduledNoticeTranscriptMetadata(t *testing.T) {
	for _, late := range []bool{true, false} {
		t.Run(fmt.Sprint("late=", late), func(t *testing.T) {
			h := newSchedHarness(t, newMockProvider(simpleResponseHandler("ok")), "2026-09-30T00:50:00Z")
			h.start()
			sess := h.session()
			r := h.repo()
			def := onceAt(mustUTC("2026-09-30T01:00:00Z"), toSession(sess.ID), tasks.Delivery{})
			def.TZ = "Europe/Madrid"
			tmpl := mkTemplate(t, r, "nightly", def)
			want := "Scheduled task · nightly · due 03:00, sent 03:00"
			if late {
				h.clock.Set(mustUTC("2026-09-30T07:14:00Z"))
				h.pass()
				o := oneRun(t, r, tmpl.ID)
				if _, err := r.ConfirmOccurrence(bgc, o.ID, o.Revision, tasks.LateRun); err != nil {
					t.Fatal(err)
				}
				want = "Scheduled task · nightly · due 03:00, sent 09:14 after your OK"
			} else {
				h.clock.Set(mustUTC("2026-09-30T01:00:00Z"))
			}
			h.pass()
			o := assignedAndDelivered(t, h, r, oneRun(t, r, tmpl.ID).ID)
			check := func(where string, c map[string]any) {
				t.Helper()
				if c["source"] != "event" || c["source_name"] != "tasks" || c["id"] != o.NoticeID || c["title"] != want {
					t.Fatalf("%s custom = %+v", where, c)
				}
				if jsonInt(c["task_id"]) != o.ChildTaskID || jsonInt(c["parent_task_id"]) != tmpl.ID ||
					jsonInt(c["occurrence_id"]) != o.ID || c["tz"] != "Europe/Madrid" ||
					jsonInt(c["due_at"]) != mustUTC("2026-09-30T01:00:00Z").UnixMilli() ||
					jsonInt(c["sent_at"]) != h.clock.Now().UnixMilli() {
					t.Fatalf("%s custom = %+v", where, c)
				}
				if _, has := c["confirmed_at"]; has != late {
					t.Fatalf("%s confirmed_at present=%v", where, has)
				}
			}
			msgs := noticeMessages(sess.History(), o.NoticeID)
			if len(msgs) != 1 {
				t.Fatalf("%d live messages", len(msgs))
			}
			check("live", msgs[0].Custom)
			saved, _, err := session.FindSessionReadOnly(h.base, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range saved.Entries {
				if e.Message.Custom["id"] == o.NoticeID {
					check("saved", e.Message.Custom)
					found = true
				}
			}
			for _, m := range saved.Messages {
				if m.Custom["id"] == o.NoticeID {
					check("saved", m.Custom)
					found = true
				}
			}
			if !found {
				t.Fatal("assignment not in the saved transcript")
			}
		})
	}
}

// Stop discards the queue: a scheduled steer is kept as one appended
// message, never returned to the composer, and starts no turn.
func TestScheduledNoticeStopKeepsOneAppend(t *testing.T) {
	prov := newMockProvider(delayedResponseHandler(5*time.Second, "slow"))
	h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
	h.start()
	srv := httptest.NewServer(NewServer(h.mgr))
	defer srv.Close()
	sess := h.session()
	if _, _, _, err := h.mgr.Send(sess.ID, "start", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, 2*time.Second, "running", func() bool { return sessState(sess) == StateRunning })
	r := h.repo()
	o := fireOnce(t, h, r, "steered", toSession(sess.ID), tasks.Delivery{})
	pollUntil(t, 10*time.Second, "steer queued", func() bool { return steerQueued(sess, o.NoticeID) })
	resp := mustAPI(t, srv, "POST", "/api/sessions/"+sess.ID+"/cancel-and-recall", "", http.StatusOK)
	out := decode[struct {
		IDs []string `json:"discarded_steer_ids"`
	}](t, resp)
	if ids := out.IDs; len(ids) != 0 {
		t.Fatalf("scheduled steer recalled to the composer: %v", ids)
	}
	pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })
	calls := prov.calls.Load()
	waitNoticeByID(t, r, o.NoticeID, tasks.NoticeDelivered)
	msgs := noticeMessages(sess.History(), o.NoticeID)
	if len(msgs) != 1 || msgs[0].Custom["autorun"] != false {
		t.Fatalf("appended = %+v", msgs)
	}
	if hasAssistantAfter(sess.History(), o.NoticeID) || prov.calls.Load() != calls {
		t.Fatal("the kept assignment started a turn")
	}
	if child, err := r.Get(bgc, o.ChildTaskID); err != nil || child.Status == tasks.StatusDone {
		t.Fatalf("child = %+v, %v", child, err)
	}
}
