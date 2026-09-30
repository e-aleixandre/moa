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

// Q3: completing an old child of a once template does not finish the
// template while a newer accepted date is still pending.
func TestScheduleReviewOldCompletionKeepsNewOnceDate(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
	tmpl := mkSchedule(t, r, "explicitly rescheduled", onceDef(utc("2026-10-01T09:00:00Z"), ""))
	first, err := r.RunNow(bg, tmpl.ID, tmpl.Revision, tmpl.Next)
	if err != nil {
		t.Fatal(err)
	}
	first = mustAssign(t, r, first.ID, "s1")
	cur := mustGet(t, r, tmpl.ID)
	if _, err = r.Update(bg, cur.ID, cur.Revision, Patch{Schedule: onceDef(utc("2026-10-02T09:00:00Z"), "")}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.AgentDone(bg, actor("s1", "p"), first.ChildTaskID); err != nil {
		t.Fatal(err)
	}
	after := mustGet(t, r, tmpl.ID)
	clock.Set(utc("2026-10-02T09:00:00Z"))
	os := materialize(t, r)
	if after.Status == StatusDone || len(os) != 1 {
		t.Fatalf("old completion canceled the accepted new date: status=%s next=%d; new runs=%d", after.Status, after.Next, len(os))
	}
	second := mustAssign(t, r, os[0].ID, "s1")
	if _, err = r.AgentDone(bg, actor("s1", "p"), second.ChildTaskID); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, r, tmpl.ID); got.Status != StatusDone {
		t.Errorf("completing the latest run left the once template %s", got.Status)
	}
}

// Q3: completing the first run does not mark the template Done while its
// second execution is still assigned.
func TestScheduleReviewOnceSecondRunNotPrematurelyDone(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
	tmpl := mkSchedule(t, r, "one shot", onceDef(utc("2026-10-01T09:00:00Z"), ""))
	first, err := r.RunNow(bg, tmpl.ID, tmpl.Revision, tmpl.Next)
	if err != nil {
		t.Fatal(err)
	}
	first = mustAssign(t, r, first.ID, "s1")
	cur := mustGet(t, r, tmpl.ID)
	if _, err := r.Update(bg, cur.ID, cur.Revision, Patch{Schedule: onceDef(utc("2026-10-02T09:00:00Z"), "")}); err != nil {
		t.Fatal(err)
	}
	clock.Set(utc("2026-10-02T09:00:00Z"))
	second := mustAssign(t, r, materializeOne(t, r).ID, "s1")
	if _, err := r.AgentDone(bg, actor("s1", "p"), first.ChildTaskID); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, r, tmpl.ID); got.Status == StatusDone && mustOcc(t, r, second.ID).State == OccAssigned {
		t.Error("first completion marked the once template Done while its second run is still assigned")
	}
}

// A ready run of a deleted template that is found late at restart cannot
// become a decision nobody can reach: it is settled instead.
func TestScheduleReviewDeletedReadyDoesNotRegateToOrphan(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
	tmpl := mkSchedule(t, r, "deleted pending", onceDef(utc("2026-09-30T09:00:00Z"), ""))
	clock.Set(utc("2026-09-30T09:00:00Z"))
	o := materializeOne(t, r)
	cur := mustGet(t, r, tmpl.ID)
	if err := r.Delete(bg, cur.ID, cur.Revision, ""); err != nil {
		t.Fatal(err)
	}
	clock.Set(utc("2026-09-30T09:20:00Z"))
	fresh := openRepo(t, r.Path())
	fresh.SetClock(clock.Now)
	if _, err := fresh.RegateOnRestart(bg); err != nil {
		t.Fatal(err)
	}
	list, err := fresh.List(bg, Filter{IncludeAgents: true})
	if err != nil {
		t.Fatal(err)
	}
	got := mustOcc(t, fresh, o.ID)
	if got.State == OccLate || list.Counts.LateOccurrences != 0 {
		t.Errorf("restart left an unreachable decision: run %s/%s, late_occurrences=%d", got.State, got.Reason, list.Counts.LateOccurrences)
	}
	if got.State != OccSkipped || got.Reason != ReasonScheduleDeleted {
		t.Errorf("run = %s/%s, want skipped/%s", got.State, got.Reason, ReasonScheduleDeleted)
	}
}

// An orphan late left by an earlier binary is settled at the next restart.
func TestScheduleReviewExistingOrphanLateIsSettledAtRestart(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
	tmpl := mkSchedule(t, r, "deleted pending", onceDef(utc("2026-09-30T09:00:00Z"), ""))
	clock.Set(utc("2026-09-30T09:00:00Z"))
	o := materializeOne(t, r)
	cur := mustGet(t, r, tmpl.ID)
	if err := r.Delete(bg, cur.ID, cur.Revision, ""); err != nil {
		t.Fatal(err)
	}
	execSQL(t, r, "UPDATE task_occurrences SET state = 'late' WHERE id = ?", o.ID)
	fresh := openRepo(t, r.Path())
	fresh.SetClock(clock.Now)
	if _, err := fresh.RegateOnRestart(bg); err != nil {
		t.Fatal(err)
	}
	list, err := fresh.List(bg, Filter{IncludeAgents: true})
	if err != nil {
		t.Fatal(err)
	}
	if list.Counts.LateOccurrences != 0 || list.Counts.Attention != 0 {
		t.Errorf("orphan late still counted: %+v", list.Counts)
	}
}

func execSQL(t *testing.T, r *Repo, stmt string, args ...any) {
	t.Helper()
	if _, err := rawDB(t, r.Path()).Exec(stmt, args...); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
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
