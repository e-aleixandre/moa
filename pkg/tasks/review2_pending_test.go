package tasks

import "testing"

// The owner's Q2 says one pending run always, including explicit runs that
// still wait in hold or wait; authorization is not delivery.
func TestReview2RecurringAuthorizedButUndeliveredIsCoalesced(t *testing.T) {
	for _, trigger := range []string{TriggerRunNow, "confirmed"} {
		t.Run(trigger, func(t *testing.T) {
			r, clock := schedRepo(t, "2026-09-28T08:00:00Z")
			tmpl := mkSchedule(t, r, "one pending always", dailyDef(9, 0, "UTC", LateAsk))
			var old Occurrence
			var err error
			if trigger == TriggerRunNow {
				old, err = r.RunNow(bg, tmpl.ID, tmpl.Revision, tmpl.Next)
			} else {
				clock.Set(utc("2026-09-28T09:20:00Z"))
				old = materializeOne(t, r)
				if old.State != OccLate {
					t.Fatalf("setup late: %+v", old)
				}
				old, err = r.ConfirmOccurrence(bg, old.ID, old.Revision, LateRun)
			}
			if err != nil {
				t.Fatal(err)
			}
			old = mustAssign(t, r, old.ID, "s1")
			if ok, err := r.SetNoticeState(bg, old.NoticeID, NoticeChange{From: []string{NoticePending}, State: NoticeHeld}); err != nil || !ok {
				t.Fatalf("hold: %v %v", ok, err)
			}
			clock.Set(utc("2026-09-29T09:00:00Z"))
			latest := mustAssign(t, r, materializeOne(t, r).ID, "s1")
			open, err := r.OpenNotices(bg)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("old state=%s trigger=%s admitted=%d confirmed=%d; latest=%d missed=%d; open notices=%d", mustOcc(t, r, old.ID).State, old.Trigger, old.AdmittedAt, old.ConfirmedAt, latest.ID, latest.MissedCount, len(open))
			if got := mustOcc(t, r, old.ID); got.State != OccSkipped || got.Reason != ReasonSuperseded || len(open) != 1 || latest.MissedCount != 1 {
				t.Fatalf("Q2: authorized but undelivered old run survives the next slot: %+v; latest=%+v notices=%d", got, latest, len(open))
			}
		})
	}
}

// Q2: a run whose delivery is uncertain (reserved without an acknowledgment,
// or already an uncertain decision) is superseded by the next slot into
// history: it is not retried, and it is not counted as missed, since it may
// have run.
func TestRound3SupersededUncertainIsHistory(t *testing.T) {
	for _, stage := range []string{"reserved", "decision"} {
		t.Run(stage, func(t *testing.T) {
			r, clock := schedRepo(t, "2026-09-28T08:00:00Z")
			tmpl := mkSchedule(t, r, "uncertain then next slot", dailyDef(9, 0, "UTC", LateAsk))
			clock.Set(utc("2026-09-28T09:00:00Z"))
			old := mustAssign(t, r, materializeOne(t, r).ID, "s1")
			if ok, err := r.SetNoticeState(bg, old.NoticeID, NoticeChange{From: []string{NoticePending}, State: NoticeSent}); err != nil || !ok {
				t.Fatalf("reserve: %v %v", ok, err)
			}
			if stage == "decision" {
				if ok, err := r.MarkDeliveryUncertain(bg, old.NoticeID); err != nil || !ok {
					t.Fatalf("uncertain: %v %v", ok, err)
				}
			}
			clock.Set(utc("2026-09-29T09:00:00Z"))
			latest := materializeOne(t, r)
			got, n := mustOcc(t, r, old.ID), mustNotice(t, r, old.NoticeID)
			if got.State != OccSkipped || got.Reason != ReasonSuperseded || n.State != NoticeFailed || latest.MissedCount != 0 {
				t.Fatalf("uncertain run = %s/%s notice %s/%s, latest missed=%d; want superseded history, missed 0",
					got.State, got.Reason, n.State, n.Reason, latest.MissedCount)
			}
			if late, _ := r.LateRuns(bg, tmpl.ID); len(late) != 0 {
				t.Fatalf("decisions left: %+v", late)
			}
			if open, _ := r.OpenNotices(bg); len(open) != 0 {
				t.Fatalf("open notices: %+v", open)
			}
		})
	}
}

// Q2: Run now is a new slot too: it replaces the candidate still waiting.
func TestRound3RunNowSupersedesWaitingRun(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-28T08:00:00Z")
	tmpl := mkSchedule(t, r, "run now over a held run", dailyDef(9, 0, "UTC", LateAsk))
	clock.Set(utc("2026-09-28T09:00:00Z"))
	old := mustAssign(t, r, materializeOne(t, r).ID, "s1")
	if ok, err := r.SetNoticeState(bg, old.NoticeID, NoticeChange{From: []string{NoticePending}, State: NoticeHeld}); err != nil || !ok {
		t.Fatalf("hold: %v %v", ok, err)
	}
	cur, err := r.Get(bg, tmpl.ID)
	if err != nil {
		t.Fatal(err)
	}
	now, err := r.RunNow(bg, tmpl.ID, cur.Revision, cur.Next)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustOcc(t, r, old.ID); got.State != OccSkipped || got.Reason != ReasonSuperseded || now.MissedCount != 1 {
		t.Fatalf("held run survived Run now: %+v; run now missed=%d", got, now.MissedCount)
	}
}
