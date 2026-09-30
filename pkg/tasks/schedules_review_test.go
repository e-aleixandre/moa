package tasks

import (
	"testing"
)

// Regression tests for the adversarial review of 43c0d909. They use only
// temporary SQLite repositories and manually advanced clocks.

// Q2: a recurring run materialized before a downtime and never admitted is
// coalesced into the latest caught-up run, which counts it as skipped.
func TestScheduleReviewCatchUpSupersedesUndeliveredRun(t *testing.T) {
	for _, initial := range []string{OccReady, OccAssigned} {
		t.Run(initial, func(t *testing.T) {
			r, clock := schedRepo(t, "2026-09-28T08:00:00Z")
			tmpl := mkSchedule(t, r, "missed across T0", dailyDef(9, 0, "UTC", LateAsk))
			clock.Set(utc("2026-09-28T09:00:00Z"))
			old := materializeOne(t, r)
			if initial == OccAssigned {
				old = mustAssign(t, r, old.ID, "s1")
			}
			clock.Set(utc("2026-10-01T09:20:00Z"))
			fresh := openRepo(t, r.Path())
			fresh.SetClock(clock.Now)
			if _, err := fresh.RegateOnRestart(bg); err != nil {
				t.Fatal(err)
			}
			latest := materializeOne(t, fresh)
			if latest.ScheduleTaskID != tmpl.ID || latest.DueAt != ms("2026-10-01T09:00:00Z") {
				t.Fatalf("latest date = %+v", latest)
			}
			list, err := fresh.List(bg, Filter{IncludeAgents: true})
			if err != nil {
				t.Fatal(err)
			}
			got := mustOcc(t, fresh, old.ID)
			if list.Counts.LateOccurrences != 1 {
				t.Errorf("catch-up left %d decisions for one recurring template, want the latest only", list.Counts.LateOccurrences)
			}
			if latest.MissedCount != 3 {
				t.Errorf("latest missed_count = %d, want 3 including the undelivered old run", latest.MissedCount)
			}
			if got.State != OccSkipped || got.Reason != ReasonSuperseded || got.ChildTaskID != 0 {
				t.Errorf("old run = %s/%s child=%d, want skipped/superseded without a child", got.State, got.Reason, got.ChildTaskID)
			}
			if old.NoticeID != "" {
				if n := mustNotice(t, fresh, old.NoticeID); n.State != NoticeFailed {
					t.Errorf("old notice = %s/%s, want failed", n.State, n.Reason)
				}
			}
		})
	}
}

// Q2: a run whose assignment is already on its way (sent) or admitted is
// never superseded: only never-admitted, unconfirmed runs are.
func TestScheduleReviewCatchUpKeepsInflightAndConfirmedRuns(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-28T08:00:00Z")
	mkSchedule(t, r, "inflight", dailyDef(9, 0, "UTC", LateRun))
	clock.Set(utc("2026-09-28T09:00:00Z"))
	sent := mustAssign(t, r, materializeOne(t, r).ID, "s1")
	if _, err := r.SetNoticeState(bg, sent.NoticeID, NoticeChange{From: []string{NoticePending}, State: NoticeSent}); err != nil {
		t.Fatal(err)
	}
	clock.Set(utc("2026-09-30T09:20:00Z"))
	latest := materializeOne(t, r)
	if got := mustOcc(t, r, sent.ID); got.State != OccAssigned {
		t.Errorf("a sent assignment was superseded: %s/%s", got.State, got.Reason)
	}
	if latest.MissedCount != 1 {
		t.Errorf("missed_count = %d, want 1 (only the empty slot of 09-29)", latest.MissedCount)
	}
}

// Q2 without a restart: an assignment still waiting in the outbox (held for a
// closed session, say) is coalesced into the next slot's run.
func TestScheduleReviewNextSlotSupersedesWaitingAssignment(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-28T08:00:00Z")
	mkSchedule(t, r, "held", dailyDef(9, 0, "UTC", LateRun))
	clock.Set(utc("2026-09-28T09:00:00Z"))
	old := mustAssign(t, r, materializeOne(t, r).ID, "s1")
	clock.Set(utc("2026-09-29T09:00:00Z"))
	latest := materializeOne(t, r)
	got := mustOcc(t, r, old.ID)
	if got.State != OccSkipped || got.Reason != ReasonSuperseded || latest.MissedCount != 1 {
		t.Fatalf("old = %s/%s, latest missed_count = %d; want superseded and 1", got.State, got.Reason, latest.MissedCount)
	}
	if n := mustNotice(t, r, old.NoticeID); n.State != NoticeFailed || n.Reason != ReasonSuperseded {
		t.Errorf("old notice = %s/%s, want failed/superseded", n.State, n.Reason)
	}
	if countRows(t, r, "tasks", "id = ?", old.ChildTaskID) != 0 {
		t.Error("the superseded run's undelivered child was kept")
	}
}
