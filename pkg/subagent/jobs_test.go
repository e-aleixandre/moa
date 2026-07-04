package subagent

import (
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
