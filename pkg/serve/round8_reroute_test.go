package serve

import (
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/tasks"
)

// After a reroute to B, a gate and the owner's Run deliver to B again; they
// never recreate the original session A (Sol review 6, P1).
func TestRound8RerouteRestartConfirmationKeepsB(t *testing.T) {
	for _, gate := range []string{"regate", "uncertain"} {
		t.Run(gate, func(t *testing.T) {
			h, r := round7Fixture(t)
			o := readyNewRun(t, h, r, newTarget(t, h.root))
			a := round7Created(t, h, r, o, "original A", false)
			round7Write(t, round7Path(t, h, o, a), []byte(`{"id":"`+a+`"`), time.Time{})
			if h.mgr.planner.provisionNew(bgc, o) {
				t.Fatal("setup: damaged A was assigned")
			}
			failed := occNow(t, r, o.ID)
			b := h.savedSession()
			dest, reason, err := h.mgr.sessionDestination(b)
			if err != nil || reason != "" {
				t.Fatalf("B destination: %s, %v", reason, err)
			}
			rerouted, err := r.RerouteOccurrence(bgc, o.ID, failed.Revision, dest)
			if err != nil || rerouted.SessionID != b {
				t.Fatalf("reroute=%+v, %v", rerouted, err)
			}
			if gate == "regate" {
				h.clock.Advance(12 * time.Minute)
			} else {
				if _, err := r.SetNoticeState(bgc, rerouted.NoticeID, tasks.NoticeChange{From: []string{tasks.NoticePending}, State: tasks.NoticeSent}); err != nil {
					t.Fatal(err)
				}
				if ok, err := r.MarkDeliveryUncertain(bgc, rerouted.NoticeID); err != nil || !ok {
					t.Fatalf("uncertain=%t, %v", ok, err)
				}
			}
			m := round5Restart(t, h)
			late := occNow(t, r, o.ID)
			if late.State != tasks.OccLate {
				t.Fatalf("setup: restart did not gate run: %+v", late)
			}
			ready, err := r.ConfirmOccurrence(bgc, late.ID, late.Revision, tasks.LateRun)
			if err != nil {
				t.Fatal(err)
			}
			provisioned := m.planner.provision(bgc, ready)
			got := occNow(t, r, o.ID)
			t.Logf("A=%s B=%s; %s gate recipient=%q reserved=%s; confirmation provision=%t result=%s/%s recipient=%q", a, b, gate, late.SessionID, late.ReservedSessionID, provisioned, got.State, got.Reason, got.SessionID)
			if !provisioned || got.State != tasks.OccAssigned || got.SessionID != b {
				t.Error("confirmation of rerouted B tried original damaged A instead")
			}
		})
	}
}

// A managed Delete(A) after the reroute does not touch B's run, and Run
// after a gate goes to B without bringing A back.
func TestRound8RerouteDeletedOriginalIsNotResurrected(t *testing.T) {
	for _, gate := range []string{"regate", "uncertain"} {
		t.Run(gate, func(t *testing.T) {
			h, r := round7Fixture(t)
			o := readyNewRun(t, h, r, newTarget(t, h.root))
			a := round7Created(t, h, r, o, "original A", false)
			round7Write(t, round7Path(t, h, o, a), []byte(`{"id":"`+a+`"`), time.Time{})
			if h.mgr.planner.provisionNew(bgc, o) {
				t.Fatal("setup: damaged A was assigned")
			}
			failed := occNow(t, r, o.ID)
			b := h.savedSession()
			dest, reason, err := h.mgr.sessionDestination(b)
			if err != nil || reason != "" {
				t.Fatalf("B destination: %s, %v", reason, err)
			}
			rerouted, err := r.RerouteOccurrence(bgc, o.ID, failed.Revision, dest)
			if err != nil || rerouted.SessionID != b {
				t.Fatalf("reroute=%+v, %v", rerouted, err)
			}
			if err := h.mgr.Delete(a); err != nil {
				t.Fatal(err)
			}
			if got := occNow(t, r, o.ID); got.State != tasks.OccAssigned || got.SessionID != b || round7Files(t, h.base) != 1 {
				t.Fatalf("setup: deleting original A changed rerouted B: %+v", got)
			}
			if gate == "regate" {
				h.clock.Advance(12 * time.Minute)
			} else {
				if _, err := r.SetNoticeState(bgc, rerouted.NoticeID, tasks.NoticeChange{From: []string{tasks.NoticePending}, State: tasks.NoticeSent}); err != nil {
					t.Fatal(err)
				}
				if ok, err := r.MarkDeliveryUncertain(bgc, rerouted.NoticeID); err != nil || !ok {
					t.Fatalf("uncertain=%t, %v", ok, err)
				}
			}
			m := round5Restart(t, h)
			late := occNow(t, r, o.ID)
			if late.State != tasks.OccLate {
				t.Fatalf("setup: restart did not gate run: %+v", late)
			}
			ready, err := r.ConfirmOccurrence(bgc, late.ID, late.Revision, tasks.LateRun)
			if err != nil {
				t.Fatal(err)
			}
			provisioned := m.planner.provision(bgc, ready)
			got := occNow(t, r, o.ID)
			t.Logf("managed Delete(A=%s) kept B=%s; %s -> owner Run: provision=%t state=%s recipient=%s reserved=%s files=%d", a, b, gate, provisioned, got.State, got.SessionID, got.ReservedSessionID, round7Files(t, h.base))
			if !provisioned || got.SessionID != b || round7Files(t, h.base) != 1 {
				t.Error("restart/confirmation resurrected explicitly deleted original A instead of retaining rerouted B")
			}
		})
	}
}
