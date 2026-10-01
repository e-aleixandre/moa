package tasks

import (
	"testing"
	"time"
)

// A reroute fixes the run's destination: once a gate withdraws its
// assignment, the run still belongs to the session it was rerouted to and no
// longer to the one originally reserved for it (Sol review 6, P1).
func TestRound8RerouteThenGateDeleteIdentity(t *testing.T) {
	for _, gate := range []string{"regate", "uncertain"} {
		for _, deleted := range []string{"original_A", "rerouted_B"} {
			t.Run(gate+"/"+deleted, func(t *testing.T) {
				r, clock := schedRepo(t, "2026-09-30T08:00:00Z")
				mkSchedule(t, r, "rerouted", newSessionDef(clock.Now().Add(time.Minute)))
				clock.Add(time.Minute)
				o := materializeOne(t, r)
				const a, b = "aaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbb"
				if _, err := r.ReserveSession(bg, o.ID, a); err != nil {
					t.Fatal(err)
				}
				failed, err := r.FailOccurrence(bg, o.ID, "create_failed", "round8")
				if err != nil {
					t.Fatal(err)
				}
				rerouted, err := r.RerouteOccurrence(bg, o.ID, failed.Revision, dest(b))
				if err != nil || rerouted.SessionID != b {
					t.Fatalf("reroute=%+v, %v", rerouted, err)
				}
				if gate == "regate" {
					clock.Add(12 * time.Minute)
					if n, err := r.RegateOnRestart(bg); err != nil || n != 1 {
						t.Fatalf("regate=%d, %v", n, err)
					}
				} else {
					if _, err := r.SetNoticeState(bg, rerouted.NoticeID, NoticeChange{From: []string{NoticePending}, State: NoticeSent}); err != nil {
						t.Fatal(err)
					}
					if ok, err := r.MarkDeliveryUncertain(bg, rerouted.NoticeID); err != nil || !ok {
						t.Fatalf("uncertain=%t, %v", ok, err)
					}
				}
				late := mustOcc(t, r, o.ID)
				if late.State != OccLate || late.ReservedSessionID != a {
					t.Fatalf("setup lost late decision/reservation: %+v", late)
				}
				if late.Spec.Target.Kind != TargetSession || late.Spec.Target.ID != b {
					t.Errorf("gated run targets %+v, not rerouted %s", late.Spec.Target, b)
				}
				id := a
				if deleted == "rerouted_B" {
					id = b
				}
				n, err := r.SettleSessionDeleted(bg, id)
				if err != nil {
					t.Fatal(err)
				}
				got := mustOcc(t, r, o.ID)
				if deleted == "original_A" && (n != 0 || got.State != OccLate) {
					t.Errorf("deleting A settled B's late decision after reroute: settled=%d state=%s", n, got.State)
				}
				if deleted == "rerouted_B" && (n != 1 || got.State == OccLate) {
					t.Errorf("deleting B left its rerouted late decision unsettled: settled=%d state=%s", n, got.State)
				}
			})
		}
	}
}
