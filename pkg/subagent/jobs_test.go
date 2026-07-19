package subagent

import (
	"errors"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestJobStore_SetMessages_DeepCopiesDefensively(t *testing.T) {
	s := newJobStore()
	j := s.create("task", "model-x", func() {})

	original := []core.AgentMessage{
		{Message: core.Message{
			Role:    "assistant",
			Content: []core.Content{{Type: "text", Text: "hello"}},
		}},
	}
	s.setMessages(j.id, original)

	// Mutate the original slice/content after storing — must not affect what's stored.
	original[0].Content[0].Text = "MUTATED"
	original[0].Role = "MUTATED"

	got := s.messages(j.id)
	if len(got) != 1 {
		t.Fatalf("expected 1 message, got %d", len(got))
	}
	if got[0].Role != "assistant" {
		t.Fatalf("Role = %q, want %q (mutation of original leaked in)", got[0].Role, "assistant")
	}
	if got[0].Content[0].Text != "hello" {
		t.Fatalf("Content[0].Text = %q, want %q (mutation of original leaked in)", got[0].Content[0].Text, "hello")
	}

	// Mutating the returned copy must not affect what's stored either.
	got[0].Content[0].Text = "MUTATED2"
	got2 := s.messages(j.id)
	if got2[0].Content[0].Text != "hello" {
		t.Fatalf("Content[0].Text = %q after mutating a returned copy, want %q", got2[0].Content[0].Text, "hello")
	}
}

func TestJobStore_Messages_UnknownJob(t *testing.T) {
	s := newJobStore()
	if got := s.messages("does-not-exist"); got != nil {
		t.Fatalf("expected nil for unknown job, got %#v", got)
	}
}

func TestJobStore_SnapshotLocked_DoesNotIncludeMessages(t *testing.T) {
	s := newJobStore()
	j := s.create("task", "model-x", func() {})
	s.setMessages(j.id, []core.AgentMessage{{Message: core.Message{Role: "assistant"}}})

	snap, ok := s.snapshot(j.id)
	if !ok {
		t.Fatalf("expected snapshot to exist")
	}
	// jobSnapshot has no Messages field — this is a compile-time guarantee,
	// exercised here only to document intent (snapshot must stay cheap).
	_ = snap
}

func TestJobStore_RunningCount_ExcludesSync(t *testing.T) {
	s := newJobStore()
	s.create("async1", "m", func() {})    // async, running
	s.create("async2", "m", func() {})    // async, running
	s.createSync("sync1", "m", func() {}) // sync, running

	if got := s.runningCount(); got != 2 {
		t.Fatalf("runningCount = %d, want 2 (sync excluded)", got)
	}
}

// TestJobStore_AccentIndex_IsCreationOrdinalNotReused verifies accentIndex is
// assigned in creation order and is never reused when an earlier job is
// removed from the store — the property the reconnect-color-stability fix
// depends on (position-in-map would renumber survivors after a delete).
func TestJobStore_AccentIndex_IsCreationOrdinalNotReused(t *testing.T) {
	s := newJobStore()
	j0 := s.create("first", "m", func() {})
	j1 := s.create("second", "m", func() {})
	j2 := s.create("third", "m", func() {})

	if j0.accentIndex != 0 || j1.accentIndex != 1 || j2.accentIndex != 2 {
		t.Fatalf("accentIndex = %d,%d,%d, want 0,1,2", j0.accentIndex, j1.accentIndex, j2.accentIndex)
	}

	// Remove the first job (as if it finished and was cleaned up), then
	// create a new one — its ordinal must not backfill the freed slot.
	s.delete(j0.id)
	j3 := s.create("fourth", "m", func() {})
	if j3.accentIndex != 3 {
		t.Fatalf("accentIndex after delete+create = %d, want 3 (no reuse)", j3.accentIndex)
	}

	snap1, ok := s.snapshot(j1.id)
	if !ok || snap1.AccentIndex != 1 {
		t.Fatalf("survivor snapshot AccentIndex = %v (ok=%v), want 1 unchanged", snap1.AccentIndex, ok)
	}
}

func TestJobs_Cancel_ReportsExistence(t *testing.T) {
	s := newJobStore()
	j := s.create("task", "m", func() {})
	handle := &Jobs{store: s}

	if !handle.Cancel(j.id) {
		t.Fatal("Cancel(existing) = false, want true")
	}
	if handle.Cancel("sa-does-not-exist") {
		t.Fatal("Cancel(missing) = true, want false")
	}
}

func TestJobStore_Promote_FlipsSyncAndClosesPromoted(t *testing.T) {
	s := newJobStore()
	j := s.createSync("task", "m", func() {})

	if err := s.promote(j.id); err != nil {
		t.Fatalf("promote() error = %v, want nil", err)
	}
	if j.isSync() {
		t.Fatal("isSync() = true after promote, want false")
	}
	select {
	case <-j.promoted:
	default:
		t.Fatal("promoted channel not closed after promote()")
	}
}

func TestJobStore_Promote_NonSyncReturnsErrNotSync(t *testing.T) {
	s := newJobStore()
	j := s.create("task", "m", func() {}) // async, not sync

	if err := s.promote(j.id); !errors.Is(err, ErrNotSync) {
		t.Fatalf("promote() error = %v, want ErrNotSync", err)
	}
}

func TestJobStore_Promote_NotRunningReturnsErrNotRunning(t *testing.T) {
	s := newJobStore()
	j := s.createSync("task", "m", func() {})
	s.setCompleted(j.id, "done")

	if err := s.promote(j.id); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("promote() error = %v, want ErrNotRunning", err)
	}
}

func TestJobStore_Promote_UnknownJobReturnsErrUnknownJob(t *testing.T) {
	s := newJobStore()
	if err := s.promote("sa-does-not-exist"); !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("promote() error = %v, want ErrUnknownJob", err)
	}
}

func TestJobStore_RunningCount_CountsPromotedJob(t *testing.T) {
	s := newJobStore()
	j := s.createSync("task", "m", func() {})

	if got := s.runningCount(); got != 0 {
		t.Fatalf("runningCount = %d before promote, want 0 (sync excluded)", got)
	}
	if err := s.promote(j.id); err != nil {
		t.Fatalf("promote() error = %v", err)
	}
	if got := s.runningCount(); got != 1 {
		t.Fatalf("runningCount = %d after promote, want 1", got)
	}
}

func TestJobs_Promote_Wrapper(t *testing.T) {
	s := newJobStore()
	j := s.createSync("task", "m", func() {})
	handle := &Jobs{store: s}

	if err := handle.Promote(j.id); err != nil {
		t.Fatalf("Promote() error = %v, want nil", err)
	}
	if err := handle.Promote("sa-does-not-exist"); !errors.Is(err, ErrUnknownJob) {
		t.Fatalf("Promote(missing) error = %v, want ErrUnknownJob", err)
	}
}
