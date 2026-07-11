package ops

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	if err := s.RecordMilestone("passed", Milestone{Type: MilestoneRunStarted, At: stamp(1), RefID: "run"}); err != nil {
		t.Fatal(err)
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

func TestPulsePagePaginatesEqualTimestampsWithoutGapsOrDuplicates(t *testing.T) {
	s := New(Config{MaxMilestones: 256})
	addSession(t, s, "session", "/work/a")
	for i := 0; i < 130; i++ {
		if err := s.RecordMilestone("session", Milestone{Type: MilestoneRunStarted, At: stamp(1), RefID: fmt.Sprintf("event-%03d", i)}); err != nil {
			t.Fatal(err)
		}
	}

	cursor := ""
	seen := make(map[string]struct{})
	finalCursor := ""
	for page := 0; ; page++ {
		pulse, err := s.PulsePage(cursor, stamp(2))
		if err != nil {
			t.Fatal(err)
		}
		if len(pulse.Changes.Items) > maxChangesMilestones {
			t.Fatalf("page has %d items", len(pulse.Changes.Items))
		}
		for _, item := range pulse.Changes.Items {
			want := fmt.Sprintf("event-%03d", len(seen))
			if got := item.Facts[0].RefID; got != want {
				t.Fatalf("item order = %q, want %q", got, want)
			}
			if _, duplicate := seen[item.ID]; duplicate {
				t.Fatalf("duplicate item %q on page %d", item.ID, page)
			}
			seen[item.ID] = struct{}{}
		}
		if !pulse.Changes.HasMore {
			if pulse.Changes.NextCursor == "" {
				t.Fatal("final page omitted polling continuation")
			}
			finalCursor = pulse.Changes.NextCursor
			break
		}
		if pulse.Changes.NextCursor == "" {
			t.Fatal("non-final page omitted continuation")
		}
		cursor = pulse.Changes.NextCursor
	}
	if len(seen) != 130 {
		t.Fatalf("received %d items, want 130", len(seen))
	}
	if err := s.RecordMilestone("session", Milestone{Type: MilestoneRunStarted, At: stamp(1), RefID: "event-130"}); err != nil {
		t.Fatal(err)
	}
	pulse, err := s.PulsePage(finalCursor, stamp(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(pulse.Changes.Items) != 1 || pulse.Changes.Items[0].Facts[0].RefID != "event-130" {
		t.Fatalf("polling continuation lost retained event: %#v", pulse.Changes)
	}
}

func TestPulsePageCursorIsOpaqueTamperSafeAndDetectsRetentionGap(t *testing.T) {
	s := New(Config{MaxMilestones: 128})
	addSession(t, s, "session", "/work/a")
	for i := 0; i < 130; i++ {
		if err := s.RecordMilestone("session", Milestone{Type: MilestoneRunStarted, At: stamp(1), RefID: fmt.Sprintf("first-%03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	pulse, err := s.PulsePage("", stamp(2))
	if err != nil {
		t.Fatal(err)
	}
	cursor := pulse.Changes.NextCursor
	if cursor == "" || strings.Contains(cursor, "session") || strings.Contains(cursor, "first") {
		t.Fatalf("cursor is not opaque: %q", cursor)
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) != 48 {
		t.Fatalf("cursor wire format = %q, %v", cursor, err)
	}
	encoded, err := json.Marshal(pulse)
	if err != nil || !strings.Contains(string(encoded), cursor) || strings.Contains(string(encoded), "global_sequence") || strings.Contains(string(encoded), "pulse_cursor_key") {
		t.Fatalf("cursor response leaked internal cursor state: %s, %v", encoded, err)
	}
	tampered := cursor[:len(cursor)-1] + "A"
	if tampered == cursor {
		tampered = cursor[:len(cursor)-1] + "B"
	}
	if _, err := s.PulsePage(tampered, stamp(2)); !errors.Is(err, ErrInvalidPulseCursor) {
		t.Fatalf("tampered cursor error = %v", err)
	}
	for i := 0; i < 65; i++ {
		if err := s.RecordMilestone("session", Milestone{Type: MilestoneRunStarted, At: stamp(3), RefID: fmt.Sprintf("later-%03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PulsePage(cursor, stamp(4)); !errors.Is(err, ErrRetentionGap) {
		t.Fatalf("retained page error = %v", err)
	}
}

func TestPulseOnTrackRequiresVerificationForCurrentRun(t *testing.T) {
	s := New(Config{})
	addSession(t, s, "session", "/work/a")
	if err := s.UpdateLifecycle("session", LifecycleUpdate{State: LifecycleRunning, Activity: ActivityRunning, At: stamp(1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateVerification("session", Verification{State: VerificationPassed, At: stamp(1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordMilestone("session", Milestone{Type: MilestoneRunStarted, At: stamp(2), RefID: "run-2"}); err != nil {
		t.Fatal(err)
	}
	pulse, err := s.PulsePage("", stamp(3))
	if err != nil {
		t.Fatal(err)
	}
	if len(pulse.OnTrack) != 0 || len(pulse.InProgress) != 1 || pulse.InProgress[0].Verification != "" {
		t.Fatalf("prior verification survived new run: %#v", pulse)
	}
	if err := s.UpdateVerification("session", Verification{State: VerificationPassed, At: stamp(3)}); err != nil {
		t.Fatal(err)
	}
	pulse, err = s.PulsePage("", stamp(4))
	if err != nil {
		t.Fatal(err)
	}
	if len(pulse.OnTrack) != 1 || pulse.OnTrack[0].Facts[2].Kind != "verification" {
		t.Fatalf("current verification did not produce evidence-backed on_track: %#v", pulse.OnTrack)
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
	pulse, err := s.PulsePage("", stamp(2))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(pulse)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(encoded)
	for _, forbidden := range []string{"canonical_cwd", "milestones", "run_started_at", "global_sequence", "pulse_cursor_key", "transcript", "tool_args", "error_message", "log"} {
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
