package ops

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestChangesSinceIsBoundedOrderedAndDeterministic(t *testing.T) {
	s := New(Config{})
	addSession(t, s, "z", "/work/z")
	addSession(t, s, "a", "/work/a")
	for _, input := range []struct {
		id        string
		milestone Milestone
	}{
		{"z", Milestone{Type: MilestoneRunEnded, At: stamp(2), RefID: "z-end"}},
		{"a", Milestone{Type: MilestoneRunStarted, At: stamp(2), RefID: "a-start"}},
		{"a", Milestone{Type: MilestoneVerification, At: stamp(3), RefID: "verify"}},
	} {
		if err := s.RecordMilestone(input.id, input.milestone); err != nil {
			t.Fatal(err)
		}
	}
	briefing, err := s.ChangesSince(stamp(1), stamp(4))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := []string{briefing.Milestones[0].Session, briefing.Milestones[1].Session, briefing.Milestones[2].Session}, []string{"a", "z", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if briefing.Spoken != "Changes: 3 milestones across 2 sessions." {
		t.Fatalf("spoken = %q", briefing.Spoken)
	}
	if _, err := s.ChangesSince(stamp(4), stamp(1)); !errors.Is(err, ErrInvalidWindow) {
		t.Fatalf("reversed window = %v", err)
	}
	if _, err := s.ChangesSince(stamp(1), stamp(1).Add(32*24*time.Hour)); !errors.Is(err, ErrInvalidWindow) {
		t.Fatalf("long window = %v", err)
	}
	if _, err := s.ChangesSince(stamp(1).In(time.FixedZone("offset", 3600)), stamp(2)); !errors.Is(err, ErrInvalidWindow) {
		t.Fatalf("non UTC = %v", err)
	}
}

func TestCheckpointPersistsAndNormalizes(t *testing.T) {
	store, state, err := OpenStore(filepath.Join(t.TempDir(), "ops.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Persist: store.Save})
	if err := s.Restore(state); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := s.CreateCheckpoint("  Shift-1 ", stamp(1))
	if err != nil || checkpoint.Name != "shift-1" {
		t.Fatalf("checkpoint = %#v, %v", checkpoint, err)
	}
	if _, err := s.CreateCheckpoint("shift-1", stamp(2)); !errors.Is(err, ErrCheckpointExists) {
		t.Fatalf("duplicate = %v", err)
	}
	if _, err := s.CreateCheckpoint("has spaces", stamp(1)); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("unsafe name = %v", err)
	}
	_, durable, err := OpenStore(store.path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(Config{})
	if err := restarted.Restore(durable); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Checkpoints(); len(got) != 1 || got[0] != checkpoint {
		t.Fatalf("restored = %#v", got)
	}
}

func TestChangesSinceDetectsRetentionGap(t *testing.T) {
	s := New(Config{MaxMilestones: 2})
	addSession(t, s, "s", "/work/a")
	for i := 1; i <= 3; i++ {
		if err := s.RecordMilestone("s", Milestone{Type: MilestoneRunStarted, At: stamp(i), RefID: string(rune('a' + i))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ChangesSince(stamp(0), stamp(4)); !errors.Is(err, ErrRetentionGap) {
		t.Fatalf("gap = %v", err)
	}
	if got, err := s.ChangesSince(stamp(2), stamp(4)); err != nil || len(got.Milestones) != 1 || got.Milestones[0].RefID != "d" {
		t.Fatalf("retained = %#v, %v", got, err)
	}
}
