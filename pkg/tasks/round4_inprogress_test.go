package tasks

import "testing"

func TestRound4InProgressWithoutAckIsSuperseded(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-28T08:00:00Z")
	tmpl := mkSchedule(t, r, "admission ack only", dailyDef(9, 0, "UTC", LateRun))
	clock.Set(utc("2026-09-28T09:00:00Z"))
	old := mustAssign(t, r, materializeOne(t, r).ID, "s1")
	child := mustGet(t, r, old.ChildTaskID)
	if _, err := r.Update(bg, child.ID, child.Revision, Patch{Status: ptr(StatusInProgress)}); err != nil {
		t.Fatal(err)
	}
	if got := mustOcc(t, r, old.ID); got.AdmittedAt != 0 {
		t.Fatal("setup unexpectedly acknowledged admission")
	}
	clock.Set(utc("2026-09-29T09:00:00Z"))
	latest := mustAssign(t, r, materializeOne(t, r).ID, "s1")
	pending := countRows(t, r, "task_occurrences", "schedule_task_id=? AND state IN ('ready','late','assigned') AND admitted_at IS NULL", tmpl.ID)
	open, err := r.OpenNotices(bg)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("old=%s admitted=%d latest=%d pending=%d open=%d", mustOcc(t, r, old.ID).State, mustOcc(t, r, old.ID).AdmittedAt, latest.ID, pending, len(open))
	if got := mustOcc(t, r, old.ID); got.State != OccSkipped || got.Reason != ReasonSuperseded {
		t.Errorf("in-progress run without ack not superseded: %s/%s", got.State, got.Reason)
	}
	if pending > 1 {
		t.Fatalf("Q2 in-progress child without admission survives latest slot: pending=%d open=%d", pending, len(open))
	}
}
