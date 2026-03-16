package bus

import (
	"sync"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// ---------------------------------------------------------------------------
// fakePersister
// ---------------------------------------------------------------------------

type persistSnapshot struct {
	messages []core.AgentMessage
	epoch    int
	metadata map[string]any
}

type fakePersister struct {
	mu        sync.Mutex
	snapshots []persistSnapshot
}

func (fp *fakePersister) Snapshot(msgs []core.AgentMessage, epoch int, meta map[string]any) error {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	fp.snapshots = append(fp.snapshots, persistSnapshot{msgs, epoch, meta})
	return nil
}

func (fp *fakePersister) count() int {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return len(fp.snapshots)
}

func (fp *fakePersister) last() persistSnapshot {
	fp.mu.Lock()
	defer fp.mu.Unlock()
	return fp.snapshots[len(fp.snapshots)-1]
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestPersistenceReactor_SavesOnRunEnded(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{messages: []core.AgentMessage{{Message: core.Message{Role: "user"}}}}
	sctx := newTestSessionContext(b, fa)
	fp := &fakePersister{}

	registerPersistenceReactor(b, sctx, fp)

	b.Publish(RunEnded{SessionID: "s1"})
	b.Drain(time.Second)

	if fp.count() != 1 {
		t.Fatalf("expected 1 snapshot, got %d", fp.count())
	}
	snap := fp.last()
	if len(snap.messages) != 1 {
		t.Fatalf("messages len = %d", len(snap.messages))
	}
}

func TestPersistenceReactor_SavesOnConfigChanged(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	fp := &fakePersister{}

	registerPersistenceReactor(b, sctx, fp)

	b.Publish(ConfigChanged{SessionID: "s1", Model: "gpt-5"})
	b.Drain(time.Second)

	if fp.count() != 1 {
		t.Fatalf("expected 1 snapshot, got %d", fp.count())
	}
}

func TestPersistenceReactor_SavesOnCommandExecuted(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	fp := &fakePersister{}

	registerPersistenceReactor(b, sctx, fp)

	b.Publish(CommandExecuted{SessionID: "s1", Command: "compact"})
	b.Drain(time.Second)

	if fp.count() != 1 {
		t.Fatalf("expected 1 snapshot, got %d", fp.count())
	}
}

func TestPersistenceReactor_SavesOnTasksUpdated(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	fp := &fakePersister{}

	registerPersistenceReactor(b, sctx, fp)

	b.Publish(TasksUpdated{SessionID: "s1"})
	b.Drain(time.Second)

	if fp.count() != 1 {
		t.Fatalf("expected 1 snapshot, got %d", fp.count())
	}
}

func TestPersistenceReactor_SavesOnCompactionEnded(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	fp := &fakePersister{}

	registerPersistenceReactor(b, sctx, fp)

	b.Publish(CompactionEnded{SessionID: "s1"})
	b.Drain(time.Second)

	if fp.count() != 1 {
		t.Fatalf("expected 1 snapshot, got %d", fp.count())
	}
}

func TestPersistenceReactor_MultipleTriggers(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{compactionEpoch: 2}
	sctx := newTestSessionContext(b, fa)
	fp := &fakePersister{}

	registerPersistenceReactor(b, sctx, fp)

	// Rapid-fire events — all should be serialized.
	b.Publish(RunEnded{})
	b.Publish(ConfigChanged{})
	b.Publish(TasksUpdated{})
	b.Drain(time.Second)

	if fp.count() != 3 {
		t.Fatalf("expected 3 snapshots, got %d", fp.count())
	}
}

func TestCollectMetadata_Minimal(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{
		model: core.Model{ID: "claude-4", Provider: "anthropic"},
	}
	sctx := newTestSessionContext(b, fa)

	meta := collectMetadata(sctx)

	if meta["model"] != "anthropic/claude-4" {
		t.Fatalf("model = %v", meta["model"])
	}
	if meta["permission_mode"] != "yolo" {
		t.Fatalf("permission_mode = %v", meta["permission_mode"])
	}
	// No thinking set → should not be present.
	if _, ok := meta["thinking"]; ok {
		t.Fatal("thinking should not be set")
	}
}

func TestCollectMetadata_WithThinking(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{
		model:         core.Model{ID: "o3"},
		thinkingLevel: "high",
	}
	sctx := newTestSessionContext(b, fa)

	meta := collectMetadata(sctx)

	if meta["thinking"] != "high" {
		t.Fatalf("thinking = %v", meta["thinking"])
	}
	// Model without provider.
	if meta["model"] != "o3" {
		t.Fatalf("model = %v", meta["model"])
	}
}

func TestExtractFinalAssistantText(t *testing.T) {
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{{Type: "text", Text: "hello"}}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			{Type: "text", Text: "part1"},
			{Type: "text", Text: "part2"},
		}}},
	}

	got := extractFinalAssistantText(msgs)
	if got != "part1part2" {
		t.Fatalf("got %q, want %q", got, "part1part2")
	}
}

func TestExtractFinalAssistantText_Empty(t *testing.T) {
	got := extractFinalAssistantText(nil)
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestExtractFinalAssistantText_OnlyUser(t *testing.T) {
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "user"}},
	}
	got := extractFinalAssistantText(msgs)
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
