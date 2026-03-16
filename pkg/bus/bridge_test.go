package bus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/checkpoint"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tasks"
)

// ---------------------------------------------------------------------------
// fakeAgent — implements AgentController for handler tests
// ---------------------------------------------------------------------------

type fakeAgent struct {
	aborted         bool
	steered         string
	model           core.Model
	thinkingLevel   string
	messages        []core.AgentMessage
	compactionEpoch int
	resetCalled     bool
	compactCalled   bool
	compactErr      error

	setModelProvider core.Provider
	setModelModel    core.Model
	setModelErr      error

	setThinkingErr error
}

func (f *fakeAgent) Abort()              { f.aborted = true }
func (f *fakeAgent) Steer(msg string)    { f.steered = msg }
func (f *fakeAgent) Model() core.Model   { return f.model }
func (f *fakeAgent) ThinkingLevel() string { return f.thinkingLevel }
func (f *fakeAgent) Messages() []core.AgentMessage { return f.messages }
func (f *fakeAgent) CompactionEpoch() int { return f.compactionEpoch }
func (f *fakeAgent) IsRunning() bool     { return false }
func (f *fakeAgent) SystemPrompt() string { return "" }

func (f *fakeAgent) SetModel(provider core.Provider, model core.Model) error {
	f.setModelProvider = provider
	f.setModelModel = model
	if f.setModelErr != nil {
		return f.setModelErr
	}
	f.model = model
	return nil
}

func (f *fakeAgent) SetThinkingLevel(level string) error {
	if f.setThinkingErr != nil {
		return f.setThinkingErr
	}
	f.thinkingLevel = level
	return nil
}

func (f *fakeAgent) Reconfigure(provider core.Provider, model core.Model, thinkingLevel string) error {
	return nil
}

func (f *fakeAgent) SetPermissionCheck(fn func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision) error {
	return nil
}

func (f *fakeAgent) SetSystemPrompt(prompt string) error { return nil }

func (f *fakeAgent) Reset() error {
	f.resetCalled = true
	return nil
}

func (f *fakeAgent) Compact(ctx context.Context) (*core.CompactionPayload, error) {
	f.compactCalled = true
	return nil, f.compactErr
}

func (f *fakeAgent) Send(ctx context.Context, prompt string) ([]core.AgentMessage, error) {
	return nil, nil
}

func (f *fakeAgent) SendWithContent(ctx context.Context, content []core.Content) ([]core.AgentMessage, error) {
	return nil, nil
}

func (f *fakeAgent) LoadMessages(msgs []core.AgentMessage) error { return nil }
func (f *fakeAgent) AppendMessage(msg core.AgentMessage) error   { return nil }
func (f *fakeAgent) Drain(timeout time.Duration)                 {}

// ---------------------------------------------------------------------------
// fakeSubscriber — implements AgentSubscriber for bridge integration tests
// ---------------------------------------------------------------------------

type fakeSubscriber struct {
	handler func(core.AgentEvent)
}

func (fs *fakeSubscriber) Subscribe(fn func(core.AgentEvent)) func() {
	fs.handler = fn
	return func() { fs.handler = nil }
}

func (fs *fakeSubscriber) emit(e core.AgentEvent) {
	if fs.handler != nil {
		fs.handler(e)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newTestSessionContext(b EventBus, agent AgentController) *SessionContext {
	return &SessionContext{
		SessionID:  "test-session",
		SessionCtx: context.Background(),
		Bus:        b,
		Agent:      agent,
	}
}

func drainChan[T any](ch <-chan T, b EventBus, t *testing.T) T {
	t.Helper()
	b.Drain(time.Second)
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Second):
		var zero T
		t.Fatalf("timeout waiting for event of type %T", zero)
		return zero
	}
}

func expectNone[T any](ch <-chan T, b EventBus, t *testing.T) {
	t.Helper()
	b.Drain(time.Second)
	select {
	case v := <-ch:
		t.Fatalf("expected no event, got %+v", v)
	default:
		// good
	}
}

// ===========================================================================
// Bridge mapping tests (table-driven)
// ===========================================================================

func TestBridgeEvent_AgentStarted(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan AgentStarted, 1)
	b.Subscribe(func(e AgentStarted) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventStart})
	e := drainChan(got, b, t)
	if e.SessionID != "test-session" {
		t.Fatalf("SessionID = %q, want %q", e.SessionID, "test-session")
	}
}

func TestBridgeEvent_AgentEnded(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan AgentEnded, 1)
	b.Subscribe(func(e AgentEnded) { got <- e })

	msgs := []core.AgentMessage{{Message: core.Message{Role: "assistant"}}}
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventEnd, Messages: msgs})
	e := drainChan(got, b, t)
	if len(e.Messages) != 1 || e.Messages[0].Role != "assistant" {
		t.Fatalf("unexpected Messages: %+v", e.Messages)
	}
}

func TestBridgeEvent_AgentError(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan AgentError, 1)
	b.Subscribe(func(e AgentError) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventError, Error: errors.New("boom")})
	e := drainChan(got, b, t)
	if e.Err == nil || e.Err.Error() != "boom" {
		t.Fatalf("Err = %v, want 'boom'", e.Err)
	}
}

func TestBridgeEvent_TurnStarted(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan TurnStarted, 1)
	b.Subscribe(func(e TurnStarted) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventTurnStart})
	e := drainChan(got, b, t)
	if e.SessionID != "test-session" {
		t.Fatalf("SessionID = %q", e.SessionID)
	}
}

func TestBridgeEvent_TurnEnded(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan TurnEnded, 1)
	b.Subscribe(func(e TurnEnded) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventTurnEnd})
	e := drainChan(got, b, t)
	if e.SessionID != "test-session" {
		t.Fatalf("SessionID = %q", e.SessionID)
	}
}

func TestBridgeEvent_MessageStarted(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan MessageStarted, 1)
	b.Subscribe(func(e MessageStarted) { got <- e })

	msg := core.AgentMessage{Message: core.Message{Role: "assistant"}}
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventMessageStart, Message: msg})
	e := drainChan(got, b, t)
	if e.Message.Role != "assistant" {
		t.Fatalf("Message.Role = %q", e.Message.Role)
	}
}

func TestBridgeEvent_TextDelta(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan TextDelta, 1)
	b.Subscribe(func(e TextDelta) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type: core.AgentEventMessageUpdate,
		AssistantEvent: &core.AssistantEvent{
			Type:  core.ProviderEventTextDelta,
			Delta: "hello",
		},
	})
	e := drainChan(got, b, t)
	if e.Delta != "hello" {
		t.Fatalf("Delta = %q, want %q", e.Delta, "hello")
	}
}

func TestBridgeEvent_ThinkingDelta(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan ThinkingDelta, 1)
	b.Subscribe(func(e ThinkingDelta) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type: core.AgentEventMessageUpdate,
		AssistantEvent: &core.AssistantEvent{
			Type:  core.ProviderEventThinkingDelta,
			Delta: "thinking...",
		},
	})
	e := drainChan(got, b, t)
	if e.Delta != "thinking..." {
		t.Fatalf("Delta = %q", e.Delta)
	}
}

func TestBridgeEvent_MessageUpdate_NilAssistantEvent(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan TextDelta, 1)
	b.Subscribe(func(e TextDelta) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventMessageUpdate})
	expectNone(got, b, t)
}

func TestBridgeEvent_MessageEnded(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan MessageEnded, 1)
	b.Subscribe(func(e MessageEnded) { got <- e })

	msg := core.AgentMessage{Message: core.Message{
		Role: "assistant",
		Content: []core.Content{
			{Type: "text", Text: "part1"},
			{Type: "text", Text: "part2"},
			{Type: "image", Text: "ignored"},
		},
	}}
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventMessageEnd, Message: msg})
	e := drainChan(got, b, t)
	if e.FullText != "part1part2" {
		t.Fatalf("FullText = %q, want %q", e.FullText, "part1part2")
	}
}

func TestBridgeEvent_ToolExecStarted(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan ToolExecStarted, 1)
	b.Subscribe(func(e ToolExecStarted) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type:       core.AgentEventToolExecStart,
		ToolCallID: "tc-1",
		ToolName:   "read",
		Args:       map[string]any{"path": "foo.go"},
	})
	e := drainChan(got, b, t)
	if e.ToolCallID != "tc-1" || e.ToolName != "read" {
		t.Fatalf("unexpected: %+v", e)
	}
	if e.Args["path"] != "foo.go" {
		t.Fatalf("Args = %+v", e.Args)
	}
}

func TestBridgeEvent_ToolExecUpdate_WithDelta(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan ToolExecUpdate, 1)
	b.Subscribe(func(e ToolExecUpdate) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type:       core.AgentEventToolExecUpdate,
		ToolCallID: "tc-1",
		Result: &core.Result{
			Content: []core.Content{{Type: "text", Text: "output"}},
		},
	})
	e := drainChan(got, b, t)
	if e.Delta != "output" {
		t.Fatalf("Delta = %q", e.Delta)
	}
}

func TestBridgeEvent_ToolExecUpdate_EmptyResult(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan ToolExecUpdate, 1)
	b.Subscribe(func(e ToolExecUpdate) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type:       core.AgentEventToolExecUpdate,
		ToolCallID: "tc-1",
		Result:     nil,
	})
	expectNone(got, b, t)
}

func TestBridgeEvent_ToolExecEnded(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan ToolExecEnded, 1)
	b.Subscribe(func(e ToolExecEnded) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type:       core.AgentEventToolExecEnd,
		ToolCallID: "tc-1",
		ToolName:   "write",
		IsError:    true,
		Rejected:   false,
		Result: &core.Result{
			Content: []core.Content{{Type: "text", Text: "error: denied"}},
		},
	})
	e := drainChan(got, b, t)
	if e.Result != "error: denied" || !e.IsError || e.Rejected {
		t.Fatalf("unexpected: %+v", e)
	}
}

func TestBridgeEvent_ToolExecEnd_EmitsTasksUpdated(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	store := tasks.NewStore()
	store.Create("task one", "", nil)
	sctx := newTestSessionContext(b, nil)
	sctx.TaskStore = store

	gotTool := make(chan ToolExecEnded, 1)
	gotTasks := make(chan TasksUpdated, 1)
	b.Subscribe(func(e ToolExecEnded) { gotTool <- e })
	b.Subscribe(func(e TasksUpdated) { gotTasks <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type:     core.AgentEventToolExecEnd,
		ToolName: "tasks",
	})

	drainChan(gotTool, b, t)
	tu := drainChan(gotTasks, b, t)
	if len(tu.Tasks) != 1 || tu.Tasks[0].Title != "task one" {
		t.Fatalf("unexpected tasks: %+v", tu.Tasks)
	}
}

func TestBridgeEvent_ToolExecEnd_NoTaskUpdate_WrongTool(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	store := tasks.NewStore()
	sctx := newTestSessionContext(b, nil)
	sctx.TaskStore = store

	gotTasks := make(chan TasksUpdated, 1)
	b.Subscribe(func(e TasksUpdated) { gotTasks <- e })

	bridgeEvent(sctx, core.AgentEvent{
		Type:     core.AgentEventToolExecEnd,
		ToolName: "read",
	})
	expectNone(gotTasks, b, t)
}

func TestBridgeEvent_Steered(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan Steered, 1)
	b.Subscribe(func(e Steered) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventSteer, Text: "focus on X"})
	e := drainChan(got, b, t)
	if e.Text != "focus on X" {
		t.Fatalf("Text = %q", e.Text)
	}
}

func TestBridgeEvent_Steered_Suppressed(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	sctx.SteerFilter = func(text string) bool { return text != "subagent" }
	got := make(chan Steered, 1)
	b.Subscribe(func(e Steered) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventSteer, Text: "subagent"})
	expectNone(got, b, t)

	// Non-suppressed steer should still work.
	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventSteer, Text: "user steer"})
	e := drainChan(got, b, t)
	if e.Text != "user steer" {
		t.Fatalf("Text = %q", e.Text)
	}
}

func TestBridgeEvent_CompactionStarted(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan CompactionStarted, 1)
	b.Subscribe(func(e CompactionStarted) { got <- e })

	bridgeEvent(sctx, core.AgentEvent{Type: core.AgentEventCompactionStart})
	e := drainChan(got, b, t)
	if e.SessionID != "test-session" {
		t.Fatalf("SessionID = %q", e.SessionID)
	}
}

func TestBridgeEvent_CompactionEnded(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	got := make(chan CompactionEnded, 1)
	b.Subscribe(func(e CompactionEnded) { got <- e })

	payload := &core.CompactionPayload{Summary: "compacted"}
	bridgeEvent(sctx, core.AgentEvent{
		Type:       core.AgentEventCompactionEnd,
		Compaction: payload,
		Error:      errors.New("partial"),
	})
	e := drainChan(got, b, t)
	if e.Payload.Summary != "compacted" {
		t.Fatalf("Payload.Summary = %q", e.Payload.Summary)
	}
	if e.Err == nil || e.Err.Error() != "partial" {
		t.Fatalf("Err = %v", e.Err)
	}
}

// ===========================================================================
// Bridge integration test — subscribe/unsubscribe lifecycle
// ===========================================================================

func TestBridge_SubscribeAndUnsubscribe(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sctx := newTestSessionContext(b, nil)
	sub := &fakeSubscriber{}

	got := make(chan AgentStarted, 2)
	b.Subscribe(func(e AgentStarted) { got <- e })

	unsub := Bridge(sctx, sub)

	// Emit via subscriber → should appear on bus.
	sub.emit(core.AgentEvent{Type: core.AgentEventStart})
	drainChan(got, b, t)

	// Unsubscribe.
	unsub()

	// Emit again → should NOT appear.
	sub.emit(core.AgentEvent{Type: core.AgentEventStart})
	expectNone(got, b, t)
}

// ===========================================================================
// Handler tests — commands
// ===========================================================================

func TestHandler_AbortRun(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	if err := b.Execute(AbortRun{SessionID: "test-session"}); err != nil {
		t.Fatal(err)
	}
	if !fa.aborted {
		t.Fatal("Abort not called")
	}
}

func TestHandler_SteerAgent(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	if err := b.Execute(SteerAgent{Text: "focus here"}); err != nil {
		t.Fatal(err)
	}
	if fa.steered != "focus here" {
		t.Fatalf("steered = %q", fa.steered)
	}
}

func TestHandler_SetThinking(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{thinkingLevel: "low"}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	got := make(chan ConfigChanged, 1)
	b.Subscribe(func(e ConfigChanged) { got <- e })

	if err := b.Execute(SetThinking{Level: "high"}); err != nil {
		t.Fatal(err)
	}
	if fa.thinkingLevel != "high" {
		t.Fatalf("thinkingLevel = %q", fa.thinkingLevel)
	}

	e := drainChan(got, b, t)
	if e.Thinking != "high" {
		t.Fatalf("ConfigChanged.Thinking = %q", e.Thinking)
	}
}

func TestHandler_ClearSession(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	got := make(chan CommandExecuted, 1)
	b.Subscribe(func(e CommandExecuted) { got <- e })

	if err := b.Execute(ClearSession{}); err != nil {
		t.Fatal(err)
	}
	if !fa.resetCalled {
		t.Fatal("Reset not called")
	}
	e := drainChan(got, b, t)
	if e.Command != "clear" {
		t.Fatalf("Command = %q", e.Command)
	}
}

func TestHandler_CompactSession(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{
		messages: []core.AgentMessage{{Message: core.Message{Role: "user"}}},
	}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	got := make(chan CommandExecuted, 1)
	b.Subscribe(func(e CommandExecuted) { got <- e })

	if err := b.Execute(CompactSession{}); err != nil {
		t.Fatal(err)
	}
	if !fa.compactCalled {
		t.Fatal("Compact not called")
	}
	e := drainChan(got, b, t)
	if e.Command != "compact" {
		t.Fatalf("Command = %q", e.Command)
	}
	if len(e.Messages) != 1 {
		t.Fatalf("Messages len = %d", len(e.Messages))
	}
}

func TestHandler_CompactSession_Error(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{compactErr: errors.New("no context")}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	err := b.Execute(CompactSession{})
	if err == nil || err.Error() != "no context" {
		t.Fatalf("err = %v", err)
	}
}

func TestHandler_UndoLastChange(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)

	// Create a temp file, checkpoint it, modify it, then undo.
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(filePath, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}

	store := checkpoint.New(5)
	store.Begin("turn 1")
	if err := store.Capture(filePath); err != nil {
		t.Fatal(err)
	}
	store.Commit()

	// Overwrite the file to simulate agent modification.
	if err := os.WriteFile(filePath, []byte("modified"), 0644); err != nil {
		t.Fatal(err)
	}

	sctx.Checkpoints = store
	RegisterHandlers(sctx)

	if err := b.Execute(UndoLastChange{}); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original" {
		t.Fatalf("content = %q, want %q", content, "original")
	}
}

func TestHandler_UndoLastChange_NoCheckpoints(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	err := b.Execute(UndoLastChange{})
	if err == nil || err.Error() != "checkpoints not available" {
		t.Fatalf("err = %v", err)
	}
}

func TestHandler_UndoLastChange_EmptyStore(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.Checkpoints = checkpoint.New(5)
	RegisterHandlers(sctx)

	err := b.Execute(UndoLastChange{})
	if err == nil {
		t.Fatal("expected error for empty checkpoint store")
	}
}

func TestHandler_MarkTaskDone(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)

	store := tasks.NewStore()
	store.Create("my task", "", nil)
	sctx.TaskStore = store
	RegisterHandlers(sctx)

	got := make(chan TasksUpdated, 1)
	b.Subscribe(func(e TasksUpdated) { got <- e })

	if err := b.Execute(MarkTaskDone{TaskID: 1}); err != nil {
		t.Fatal(err)
	}

	e := drainChan(got, b, t)
	if len(e.Tasks) != 1 || e.Tasks[0].Status != "done" {
		t.Fatalf("unexpected tasks: %+v", e.Tasks)
	}
}

func TestHandler_MarkTaskDone_NotFound(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.TaskStore = tasks.NewStore()
	RegisterHandlers(sctx)

	err := b.Execute(MarkTaskDone{TaskID: 999})
	if err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestHandler_MarkTaskDone_NoStore(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	err := b.Execute(MarkTaskDone{TaskID: 1})
	if err == nil || err.Error() != "task store not available" {
		t.Fatalf("err = %v", err)
	}
}

func TestHandler_ResetTasks(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)

	store := tasks.NewStore()
	store.Create("task A", "", nil)
	store.Create("task B", "", nil)
	sctx.TaskStore = store
	RegisterHandlers(sctx)

	got := make(chan TasksUpdated, 1)
	b.Subscribe(func(e TasksUpdated) { got <- e })

	if err := b.Execute(ResetTasks{}); err != nil {
		t.Fatal(err)
	}

	e := drainChan(got, b, t)
	if len(e.Tasks) != 0 {
		t.Fatalf("expected 0 tasks after reset, got %d", len(e.Tasks))
	}
}

func TestHandler_ResetTasks_NoStore(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	err := b.Execute(ResetTasks{})
	if err == nil || err.Error() != "task store not available" {
		t.Fatalf("err = %v", err)
	}
}

// ===========================================================================
// Handler tests — queries
// ===========================================================================

func TestQuery_GetMessages(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{messages: []core.AgentMessage{
		{Message: core.Message{Role: "user"}},
		{Message: core.Message{Role: "assistant"}},
	}}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	msgs, err := QueryTyped[GetMessages, []core.AgentMessage](b, GetMessages{})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("len = %d", len(msgs))
	}
}

func TestQuery_GetModel(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{model: core.Model{ID: "claude-4", Name: "Claude 4"}}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	m, err := QueryTyped[GetModel, core.Model](b, GetModel{})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "claude-4" {
		t.Fatalf("Model.ID = %q", m.ID)
	}
}

func TestQuery_GetThinkingLevel(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{thinkingLevel: "medium"}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	level, err := QueryTyped[GetThinkingLevel, string](b, GetThinkingLevel{})
	if err != nil {
		t.Fatal(err)
	}
	if level != "medium" {
		t.Fatalf("level = %q", level)
	}
}

func TestQuery_GetContextUsage(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{
		model: core.Model{MaxInput: 1000},
		messages: []core.AgentMessage{
			{Message: core.Message{Role: "user", Content: []core.Content{{Type: "text", Text: "hello"}}}},
		},
	}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	pct, err := QueryTyped[GetContextUsage, int](b, GetContextUsage{})
	if err != nil {
		t.Fatal(err)
	}
	// We can't predict exact token estimation, but it should be >= 0 and <= 100.
	if pct < 0 || pct > 100 {
		t.Fatalf("pct = %d, want [0,100]", pct)
	}
}

func TestQuery_GetContextUsage_NoMaxInput(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{model: core.Model{MaxInput: 0}}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	pct, err := QueryTyped[GetContextUsage, int](b, GetContextUsage{})
	if err != nil {
		t.Fatal(err)
	}
	if pct != -1 {
		t.Fatalf("pct = %d, want -1", pct)
	}
}

func TestQuery_GetTasks(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	store := tasks.NewStore()
	store.Create("task A", "", nil)
	sctx.TaskStore = store
	RegisterHandlers(sctx)

	result, err := QueryTyped[GetTasks, []tasks.Task](b, GetTasks{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].Title != "task A" {
		t.Fatalf("unexpected tasks: %+v", result)
	}
}

func TestQuery_GetTasks_NilStore(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	result, err := QueryTyped[GetTasks, []tasks.Task](b, GetTasks{})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("expected nil, got %+v", result)
	}
}

func TestQuery_GetPlanMode_Nil(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	info, err := QueryTyped[GetPlanMode, PlanModeInfo](b, GetPlanMode{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode != "off" {
		t.Fatalf("Mode = %q, want %q", info.Mode, "off")
	}
}

func TestQuery_GetCompactionEpoch(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{compactionEpoch: 3}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	epoch, err := QueryTyped[GetCompactionEpoch, int](b, GetCompactionEpoch{})
	if err != nil {
		t.Fatal(err)
	}
	if epoch != 3 {
		t.Fatalf("epoch = %d, want 3", epoch)
	}
}

func TestQuery_GetPermissionMode_NoGate(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	mode, err := QueryTyped[GetPermissionMode, string](b, GetPermissionMode{})
	if err != nil {
		t.Fatal(err)
	}
	if mode != "yolo" {
		t.Fatalf("mode = %q, want %q", mode, "yolo")
	}
}

func TestQuery_GetPathPolicy_Nil(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	info, err := QueryTyped[GetPathPolicy, PathPolicyInfo](b, GetPathPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if info.WorkspaceRoot != "" || info.Scope != "" || info.AllowedPaths != nil {
		t.Fatalf("expected empty PathPolicyInfo, got %+v", info)
	}
}

// ===========================================================================
// Handler test — GetSessionState is not registered (deferred)
// ===========================================================================

func TestQuery_GetSessionState_NotRegistered(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	_, err := b.Query(GetSessionState{})
	if !errors.Is(err, ErrNoHandler) {
		t.Fatalf("expected ErrNoHandler, got %v", err)
	}
}

// ===========================================================================
// SwitchModel — requires model registry so tested with error case only
// ===========================================================================

func TestHandler_SwitchModel_Unknown(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.ProviderFactory = func(m core.Model) (core.Provider, error) {
		return nil, fmt.Errorf("no provider")
	}
	RegisterHandlers(sctx)

	err := b.Execute(SwitchModel{ModelSpec: "nonexistent-model-xyz"})
	if err == nil {
		t.Fatal("expected error for unknown model")
	}
}
