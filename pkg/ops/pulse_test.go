package ops

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPulseAttentionReasonsAreAllowedAndPriorityStable(t *testing.T) {
	s := New(Config{})
	for _, id := range []string{"z-activity", "a-lifecycle", "permission", "failed", "unknown"} {
		addSession(t, s, id, "/work/"+id)
	}
	if err := s.UpdateLifecycle("z-activity", LifecycleUpdate{State: LifecycleRunning, Activity: ActivityError, At: stamp(1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateLifecycle("a-lifecycle", LifecycleUpdate{State: LifecycleError, Activity: ActivityIdle, At: stamp(1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateLifecycle("permission", LifecycleUpdate{State: LifecycleRunning, Activity: ActivityPermission, At: stamp(1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateVerification("failed", Verification{State: VerificationFailed, At: stamp(1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateLifecycle("unknown", LifecycleUpdate{State: LifecycleRunning, Activity: ActivityRunning, At: stamp(1)}); err != nil {
		t.Fatal(err)
	}

	pulse, err := s.Pulse(nil, stamp(2))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, item := range pulse.NeedsAttention {
		got = append(got, item.Session.ID+":"+item.Category)
		if item.Priority == nil {
			t.Fatalf("attention item has no priority: %#v", item)
		}
		if len(item.Facts) > 3 || len(item.Facts) < 2 || item.Facts[0].Provenance != PulseDerived || item.Facts[1].Provenance != PulseObserved {
			t.Fatalf("attention facts are not bounded explainable evidence: %#v", item.Facts)
		}
	}
	want := []string{
		"a-lifecycle:lifecycle_error",
		"z-activity:activity_error",
		"permission:permission_needed",
		"failed:verification_failed",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attention order = %v, want %v", got, want)
	}
	if pulse.Summary.NeedsAttention != len(want) {
		t.Fatalf("attention summary = %d, want %d", pulse.Summary.NeedsAttention, len(want))
	}
}

func TestPulseActiveWorkDoesNotDescribeUnknownVerificationAsStatus(t *testing.T) {
	s := New(Config{})
	addSession(t, s, "unknown", "/work/unknown")
	addSession(t, s, "passed", "/work/passed")
	for _, id := range []string{"unknown", "passed"} {
		if err := s.UpdateLifecycle(id, LifecycleUpdate{State: LifecycleRunning, Activity: ActivityRunning, At: stamp(2)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpdateVerification("passed", Verification{State: VerificationPassed, At: stamp(2)}); err != nil {
		t.Fatal(err)
	}

	pulse, err := s.Pulse(nil, stamp(3))
	if err != nil {
		t.Fatal(err)
	}
	if len(pulse.InProgress) != 1 || pulse.InProgress[0].Session.ID != "unknown" || pulse.InProgress[0].Verification != "" {
		t.Fatalf("unknown active item = %#v", pulse.InProgress)
	}
	if len(pulse.OnTrack) != 1 || pulse.OnTrack[0].Session.ID != "passed" || pulse.OnTrack[0].Verification != VerificationPassed {
		t.Fatalf("passed active item = %#v", pulse.OnTrack)
	}
	encoded, err := json.Marshal(pulse)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"verification":"unknown"`) {
		t.Fatalf("pulse stated unknown verification: %s", encoded)
	}
}

func TestPulseChangesUseBoundedJournalAndRetention(t *testing.T) {
	s := New(Config{MaxMilestones: 2})
	addSession(t, s, "session", "/work/a")
	for i := 1; i <= 3; i++ {
		if err := s.RecordMilestone("session", Milestone{Type: MilestoneRunStarted, At: stamp(i), RefID: string(rune('a' + i))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Pulse(ptrStamp(0), stamp(4)); !errors.Is(err, ErrRetentionGap) {
		t.Fatalf("retention gap = %v", err)
	}
	pulse, err := s.Pulse(ptrStamp(2), stamp(4))
	if err != nil {
		t.Fatal(err)
	}
	if !pulse.Changes.Requested || pulse.Changes.Since == nil || len(pulse.Changes.Items) != 1 || pulse.Changes.Items[0].ID != "pulse:change:session:run_started:d" || pulse.Summary.Changes != 1 {
		t.Fatalf("changes = %#v", pulse.Changes)
	}
	if _, err := s.Pulse(ptrStamp(4), stamp(3)); !errors.Is(err, ErrInvalidWindow) {
		t.Fatalf("invalid range = %v", err)
	}
}

func TestPulseWireContainsOnlySafeProjectionFields(t *testing.T) {
	s := New(Config{})
	if err := s.UpsertSession(SessionInput{ID: "session", Title: "release", CanonicalCWD: "/work/release", Presence: PresenceActive}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateLifecycle("session", LifecycleUpdate{State: LifecycleRunning, Activity: ActivityRunning, At: stamp(1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordMilestone("session", Milestone{Type: MilestoneRunStarted, At: stamp(1), RefID: "run-1"}); err != nil {
		t.Fatal(err)
	}
	pulse, err := s.Pulse(ptrStamp(0), stamp(2))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(pulse)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(encoded)
	for _, forbidden := range []string{"canonical_cwd", "milestones", "transcript", "tool_args", "error_message", "log"} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("pulse leaked %q: %s", forbidden, wire)
		}
	}
	if !strings.Contains(wire, `"directed_instruction":{"target_id":"session"}`) || !strings.Contains(wire, `"provenance":"observed"`) {
		t.Fatalf("pulse omitted safe instruction/evidence fields: %s", wire)
	}
}

func ptrStamp(second int) *time.Time {
	at := stamp(second)
	return &at
}
