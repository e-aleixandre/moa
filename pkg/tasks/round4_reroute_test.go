package tasks

import (
	"errors"
	"testing"
)

func TestRound4RerouteOfSupersededRunConflicts(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-28T08:00:00Z")
	tmpl := mkSchedule(t, r, "one pending includes reroute", dailyDef(9, 0, "UTC", LateRun))
	clock.Set(utc("2026-09-28T09:00:00Z"))
	old := materializeOne(t, r)
	var err error
	old, err = r.FailOccurrence(bg, old.ID, ReasonSessionDeleted, "recipient deleted before assignment")
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(utc("2026-09-29T09:00:00Z"))
	latest := mustAssign(t, r, materializeOne(t, r).ID, "s1")
	rerouted, err := r.RerouteOccurrence(bg, old.ID, old.Revision, Destination{SessionID: "s2"})
	pending := countRows(t, r, "task_occurrences", "schedule_task_id=? AND state IN ('ready','late','assigned') AND admitted_at IS NULL", tmpl.ID)
	open, readErr := r.OpenNotices(bg)
	if readErr != nil {
		t.Fatal(readErr)
	}
	t.Logf("old reroute=%s/%s err=%v admitted=%d; latest=%d; pending=%d open=%d", rerouted.State, rerouted.Reason, err, rerouted.AdmittedAt, latest.ID, pending, len(open))
	if pending > 1 {
		t.Fatalf("Q2 reroute revived old failure beside latest: %d pending occurrences, %d open assignments", pending, len(open))
	}
	var conflict *OccurrenceConflictError
	if !errors.As(err, &conflict) || conflict.Error() != "A newer run replaced this one" {
		t.Fatalf("reroute of a superseded run: err=%v, want a conflict saying a newer run replaced it", err)
	}
}
