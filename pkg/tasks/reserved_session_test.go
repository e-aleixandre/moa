package tasks

import (
	"errors"
	"testing"
	"time"
)

func newSessionDef(at time.Time) *ScheduleDef {
	return &ScheduleDef{When: When{Kind: WhenOnce, At: at.UnixMilli()}, TZ: "UTC",
		Target: Target{Kind: TargetNew, Project: "p", CWD: "/work/p", Model: "m"}}
}

// A reservation is recorded once and then kept: a second candidate gets the
// first ID, and no two runs may hold the same one.
func TestReserveSessionIsStableAndUnique(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
	mkSchedule(t, r, "a", newSessionDef(clock.Now().Add(time.Minute)))
	mkSchedule(t, r, "b", newSessionDef(clock.Now().Add(time.Minute)))
	clock.Add(time.Minute)
	os := materialize(t, r)
	if len(os) != 2 {
		t.Fatalf("runs = %+v", os)
	}
	a, b := os[0], os[1]
	rev := mustOcc(t, r, a.ID).Revision
	got, err := r.ReserveSession(bg, a.ID, "aaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil || got != "aaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("reserve = %q, %v", got, err)
	}
	if again, err := r.ReserveSession(bg, a.ID, "bbbbbbbbbbbbbbbbbbbbbbbb"); err != nil || again != got {
		t.Fatalf("second reserve = %q, %v; want the first ID", again, err)
	}
	if o := mustOcc(t, r, a.ID); o.ReservedSessionID != got || o.Revision != rev || o.State != OccReady {
		t.Fatalf("run after reservation = %+v", o)
	}
	if _, err := r.ReserveSession(bg, b.ID, got); err == nil {
		t.Fatal("two runs reserved the same session ID")
	}
	// Kept whatever happens to the run.
	if _, err := r.FailOccurrence(bg, a.ID, "create_failed", ""); err != nil {
		t.Fatal(err)
	}
	if o := mustOcc(t, r, a.ID); o.ReservedSessionID != got {
		t.Fatalf("failed run lost its reservation: %+v", o)
	}
}

func TestReserveSessionOnlyForReadyNewRuns(t *testing.T) {
	r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
	mkSchedule(t, r, "existing", onceDef(clock.Now().Add(time.Minute), ""))
	clock.Add(time.Minute)
	o := materializeOne(t, r)
	_, err := r.ReserveSession(bg, o.ID, "aaaaaaaaaaaaaaaaaaaaaaaa")
	var conflict *OccurrenceConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("reserve on an existing-session run = %v", err)
	}
}
