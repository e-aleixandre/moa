package serve

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// reservedWithoutAck leaves a once run to a saved session with its
// assignment reserved (sent) and no admission recorded: an attempt that died
// between the reservation and the acknowledgment.
func reservedWithoutAck(t *testing.T, h *schedHarness, r *tasks.Repo, sid string, d tasks.Delivery) tasks.Occurrence {
	t.Helper()
	mkTemplate(t, r, "reserved without ack", onceAt(h.clock.Now().Add(time.Minute), toSession(sid), d))
	h.clock.Advance(time.Minute)
	due, err := r.MaterializeDue(bgc, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("T0: %+v %v", due, err)
	}
	o, err := r.AssignOccurrence(bgc, due[0].ID, tasks.Destination{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := r.SetNoticeState(bgc, o.NoticeID, tasks.NoticeChange{From: []string{tasks.NoticePending}, State: tasks.NoticeSent}); err != nil || !ok {
		t.Fatalf("reservation: %v %v", ok, err)
	}
	return o
}

func wantUncertain(t *testing.T, r *tasks.Repo, o tasks.Occurrence, calls int32) tasks.Occurrence {
	t.Helper()
	got := occNow(t, r, o.ID)
	n := noticeByID(t, r, o.NoticeID)
	if got.State != tasks.OccLate || got.Reason != tasks.ReasonUncertain || got.AdmittedAt != 0 || calls != 0 {
		t.Fatalf("Q1: reserved without ack = %s/%s admitted=%d calls=%d; want late/%s, never re-sent", got.State, got.Reason, got.AdmittedAt, calls, tasks.ReasonUncertain)
	}
	if n.State != tasks.NoticeFailed || n.Reason != tasks.ReasonUncertain || got.ChildTaskID != 0 || got.NoticeID != "" {
		t.Fatalf("uncertain run keeps its assignment: notice %s/%s run %+v", n.State, n.Reason, got)
	}
	return got
}

// The startup read error must not authorize an unadmitted late run when the
// recipient becomes readable again without another server restart.
func TestReview2UnreadableReservedTranscriptCannotBypassLate(t *testing.T) {
	prov := newMockProvider(simpleResponseHandler("ran without OK"))
	h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
	h.start()
	sid := h.savedSession()
	h.stop()
	r := h.repo()
	o := reservedWithoutAck(t, h, r, sid, tasks.Delivery{Saved: tasks.DeliverWake})
	_, store, err := session.FindSessionReadOnly(h.base, sid)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Dir(), sid+".json")
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("{unreadable"), 0600); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(12 * time.Minute)
	h.start()
	h.pass()
	if err = os.WriteFile(path, saved, 0600); err != nil {
		t.Fatal(err)
	}
	h.mgr.notices.trigger(func() { h.mgr.notices.reconcileAll = true })
	pollUntil(t, 10*time.Second, "late decision or unexpected admission", func() bool {
		o2 := occNow(t, r, o.ID)
		return o2.State == tasks.OccLate || o2.AdmittedAt != 0 || prov.calls.Load() > 0
	})
	wantUncertain(t, r, o, prov.calls.Load())
}

// Q1: an attempted run without a persisted acknowledgment is never run on its
// own, whatever its late policy, however soon the restart, and whatever the
// transcript says. Run sends it again (a new child and assignment); Skip
// settles it.
func TestRound3ReservedWithoutAckIsAnOwnerDecision(t *testing.T) {
	for _, tc := range []struct{ name, late, answer string }{
		{"late_run/answer_run", tasks.LateRun, tasks.LateRun},
		{"late_ask/answer_skip", tasks.LateAsk, tasks.LateSkip},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prov := newMockProvider(simpleResponseHandler("retried after OK"))
			h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
			h.start()
			sid := h.savedSession()
			h.stop()
			r := h.repo()
			o := reservedWithoutAck(t, h, r, sid, tasks.Delivery{Saved: tasks.DeliverWake, Late: tc.late})
			h.clock.Advance(time.Minute) // not late: the threshold does not matter
			h.start()
			h.pass()
			pollUntil(t, 10*time.Second, "decision or delivery", func() bool {
				got := occNow(t, r, o.ID)
				return got.State != tasks.OccAssigned || prov.calls.Load() > 0
			})
			got := wantUncertain(t, r, o, prov.calls.Load())
			rec, err := r.Get(bgc, got.ScheduleTaskID)
			if err != nil || rec.LateCount != 1 {
				t.Fatalf("uncertain decision not counted: %+v %v", rec, err)
			}
			answered, err := r.ConfirmOccurrence(bgc, got.ID, got.Revision, tc.answer)
			if err != nil {
				t.Fatal(err)
			}
			if tc.answer == tasks.LateSkip {
				if answered.State != tasks.OccSkipped || prov.calls.Load() != 0 {
					t.Fatalf("skip = %+v calls=%d", answered, prov.calls.Load())
				}
				return
			}
			h.pass()
			again := waitOcc(t, r, o.ID, "admitted after OK", func(o tasks.Occurrence) bool { return o.AdmittedAt != 0 })
			pollUntil(t, 10*time.Second, "one provider call", func() bool { return prov.calls.Load() == 1 })
			if again.ChildTaskID == 0 || again.NoticeID == o.NoticeID || again.ConfirmedAt == 0 {
				t.Fatalf("retry links: %+v (was notice %s)", again, o.NoticeID)
			}
		})
	}
}

// Q1: the transcript is not the authority for a scheduled reservation. One
// found there without an acknowledgment is still uncertain; one acknowledged
// but missing from it is never sent again.
func TestRound3AckNotTranscriptDecides(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		name := "unacknowledged_in_transcript"
		if admitted {
			name = "acknowledged_missing_from_transcript"
		}
		t.Run(name, func(t *testing.T) {
			prov := newMockProvider(simpleResponseHandler("ok"), simpleResponseHandler("sent twice"))
			h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
			h.start()
			sess := h.session()
			r := h.repo()
			o := fireOnce(t, h, r, "ack decides", toSession(sess.ID), tasks.Delivery{})
			o = waitOcc(t, r, o.ID, "admitted", func(o tasks.Occurrence) bool { return o.AdmittedAt != 0 })
			waitNoticeByID(t, r, o.NoticeID, tasks.NoticeDelivered)
			pollUntil(t, 5*time.Second, "idle", func() bool { return sessState(sess) == StateIdle })
			closeSession(t, h.mgr, sess.ID)
			h.stop()
			calls := prov.calls.Load()
			sqlExec(t, h.dbPath(), "UPDATE task_notifications SET state = 'sent', delivered_at = NULL WHERE id = ?", o.NoticeID)
			if admitted {
				_, store, err := session.FindSessionReadOnly(h.base, sess.ID)
				if err != nil {
					t.Fatal(err)
				}
				saved, err := store.LoadReadOnly(sess.ID)
				if err != nil {
					t.Fatal(err)
				}
				saved.Entries, saved.Messages, saved.LeafID = nil, nil, ""
				if err := store.Save(saved); err != nil {
					t.Fatal(err)
				}
			} else {
				sqlExec(t, h.dbPath(), "UPDATE task_occurrences SET admitted_at = NULL WHERE id = ?", o.ID)
			}
			h.clock.Advance(20 * time.Minute)
			h.start()
			h.pass()
			if admitted {
				waitNoticeByID(t, r, o.NoticeID, tasks.NoticeDelivered)
				got := occNow(t, r, o.ID)
				if got.State != tasks.OccAssigned || prov.calls.Load() != calls || transcriptNoticeCount(t, h.base, sess.ID, o.NoticeID) != 0 {
					t.Fatalf("acknowledged run sent again: %+v calls %d→%d", got, calls, prov.calls.Load())
				}
				return
			}
			pollUntil(t, 10*time.Second, "decision", func() bool { return occNow(t, r, o.ID).State != tasks.OccAssigned })
			wantUncertain(t, r, o, prov.calls.Load()-calls)
		})
	}
}

// Q1 without a restart: the periodic reconciliation finds a live session's
// reservation without an acknowledgment (its write failed). It becomes the
// owner's decision instead of being delivered again after two minutes.
func TestRound3PeriodicReconcileNeverResendsUnacknowledged(t *testing.T) {
	prov := newMockProvider(simpleResponseHandler("sent twice"))
	h := newSchedHarness(t, prov, "2026-09-30T08:00:00Z")
	h.start()
	schedReviewStopWorkers(t, h.mgr)
	sess := h.session()
	r := h.repo()
	o := reservedWithoutAck(t, h, r, sess.ID, tasks.Delivery{})
	h.clock.Advance(3 * time.Minute)
	for i := 0; i < 2; i++ {
		h.mgr.notices.trigger(func() { h.mgr.notices.reconcileAll = true })
		h.mgr.notices.pass(bgc)
	}
	wantUncertain(t, r, o, prov.calls.Load())
	if hasNotice(sess.History(), o.NoticeID) {
		t.Fatal("uncertain assignment injected")
	}
}
