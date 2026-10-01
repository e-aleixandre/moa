package tasks

import (
	"testing"
	"time"
)

// A run that is late before its first assignment has no resolved session,
// but its snapshot names the existing session it goes to: deleting that
// session settles the decision, and deleting another one does not.
func TestRound8DeleteSettlesLateRunOfItsTargetSession(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
	mkSchedule(t, r, "late", onceDef(clock.Now().Add(time.Minute), ""))
	clock.Add(13 * time.Minute)
	late := materializeOne(t, r)
	if late.State != OccLate || late.SessionID != "" || late.ReservedSessionID != "" {
		t.Fatalf("setup: %+v", late)
	}
	if n, err := r.SettleSessionDeleted(bg, "other"); err != nil || n != 0 {
		t.Fatalf("deleting another session settled %d, %v", n, err)
	}
	n, err := r.SettleSessionDeleted(bg, "s1")
	got := mustOcc(t, r, late.ID)
	if err != nil || n != 1 || got.State != OccFailed || got.Reason != ReasonSessionDeleted {
		t.Errorf("deleting the late run's target session: settled=%d err=%v run=%s/%s", n, err, got.State, got.Reason)
	}
}
