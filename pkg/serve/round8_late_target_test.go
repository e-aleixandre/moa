package serve

import (
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

// Deleting the existing session a run that was late from the start goes to
// settles its decision (Sol review 6, P2).
func TestRound8DeleteLateExistingSessionSettles(t *testing.T) {
	h, r := round7Fixture(t)
	b := h.savedSession()
	tmpl := mkTemplate(t, r, "late existing recipient", onceAt(h.clock.Now().Add(time.Minute), toSession(b), tasks.Delivery{}))
	h.clock.Advance(13 * time.Minute)
	os, err := r.MaterializeDue(bgc, 10)
	if err != nil || len(os) != 1 || os[0].State != tasks.OccLate {
		t.Fatalf("setup late run=%+v, %v", os, err)
	}
	if err := h.mgr.Delete(b); err != nil {
		t.Fatal(err)
	}
	got := oneRun(t, r, tmpl.ID)
	if got.State != tasks.OccFailed || got.Reason != tasks.ReasonSessionDeleted {
		t.Errorf("deleted recipient %s; late run left %s/%s", b, got.State, got.Reason)
	}
}
