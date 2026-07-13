package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/extension"
)

// --- MockProvider ---

// MockProvider returns scripted events. No network.
type MockProvider struct {
	mu       sync.Mutex
	calls    int
	handlers []func(req core.Request) (<-chan core.AssistantEvent, error)
}

func NewMockProvider(handlers ...func(req core.Request) (<-chan core.AssistantEvent, error)) *MockProvider {
	return &MockProvider{handlers: handlers}
}

func (m *MockProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	m.mu.Lock()
	idx := m.calls
	m.calls++
	m.mu.Unlock()

	if idx >= len(m.handlers) {
		return nil, fmt.Errorf("no more mock handlers (call %d)", idx)
	}
	return m.handlers[idx](req)
}

// alwaysProvider streams the same short text response on every call, without a
// fixed handler budget. Useful when a test drives many turns plus a compaction.
type alwaysProvider struct{ text string }

func (p *alwaysProvider) Stream(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
	ch := make(chan core.AssistantEvent, 5)
	msg := core.Message{
		Role:       "assistant",
		Content:    []core.Content{core.TextContent(p.text)},
		StopReason: "end_turn",
		Timestamp:  time.Now().Unix(),
	}
	go func() {
		defer close(ch)
		ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
		ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, ContentIndex: 0, Delta: p.text}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	}()
	return ch, nil
}

// simpleTextResponse returns a handler that streams a text response.
func simpleTextResponse(text string) func(req core.Request) (<-chan core.AssistantEvent, error) {
	return func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role:       "assistant",
				Content:    []core.Content{core.TextContent(text)},
				StopReason: "end_turn",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextStart, ContentIndex: 0}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, ContentIndex: 0, Delta: text}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextEnd, ContentIndex: 0}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}
}

// toolCallResponse returns a handler that emits a tool call, followed by a text response on the next call.
func toolCallResponse(toolID, toolName string, args map[string]any) func(req core.Request) (<-chan core.AssistantEvent, error) {
	return func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.TextContent("I'll use the tool."),
					core.ToolCallContent(toolID, toolName, args),
				},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}
}

// --- Test helpers ---

func newTestAgent(provider core.Provider, tools ...core.Tool) *Agent {
	reg := core.NewRegistry()
	for _, t := range tools {
		_ = reg.Register(t)
	}
	ag, err := New(AgentConfig{
		Provider:            provider,
		Model:               core.Model{ID: "test-model", Provider: "mock"},
		SystemPrompt:        "You are a test agent.",
		Tools:               reg,
		MaxTurns:            10,
		MaxToolCallsPerTurn: 5,
		MaxRunDuration:      30 * time.Second,
	})
	if err != nil {
		panic("newTestAgent: " + err.Error())
	}
	return ag
}

type eventCollector struct {
	mu     sync.Mutex
	events []core.AgentEvent
}

func (c *eventCollector) add(e core.AgentEvent) {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

func (c *eventCollector) snapshot() []core.AgentEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]core.AgentEvent, len(c.events))
	copy(cp, c.events)
	return cp
}

func collectEvents(ag *Agent) *eventCollector {
	c := &eventCollector{}
	ag.Subscribe(c.add)
	return c
}

// waitForEvent polls until an event of the given type appears, or timeout.
func waitForEvent(c *eventCollector, eventType string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, e := range c.snapshot() {
			if e.Type == eventType {
				return true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// --- Tests ---

func TestLoop_SimpleTextResponse(t *testing.T) {
	provider := NewMockProvider(simpleTextResponse("Hello, world!"))
	ag := newTestAgent(provider)
	events := collectEvents(ag)

	msgs, err := ag.Run(context.Background(), "Say hello")
	if err != nil {
		t.Fatal(err)
	}

	// Should have: user message + assistant message
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("unexpected roles: %s, %s", msgs[0].Role, msgs[1].Role)
	}
	if msgs[1].Content[0].Text != "Hello, world!" {
		t.Fatalf("unexpected text: %q", msgs[1].Content[0].Text)
	}

	// Should have emitted events (async delivery — poll with timeout)
	if !waitForEvent(events, core.AgentEventStart, 500*time.Millisecond) {
		t.Fatal("missing agent_start event")
	}
	if !waitForEvent(events, core.AgentEventEnd, 500*time.Millisecond) {
		t.Fatal("missing agent_end event")
	}
}

// TestIngress_AllUserMessagesGetMsgID is the contract guard for bug #13
// (compact duplicating retained user messages). Every path that inserts a
// message into agent state must leave it with a non-empty MsgID before it can
// be synced to the tree — otherwise the tree syncer first records it under a
// positional "legacy:<index>" identity, then compaction assigns it a real
// MsgID, and the next sync re-appends it as a duplicate after the compaction
// marker. Assert the invariant across Run/Send/SendWithCustom/SendWithContent
// without ever calling Messages() first (which must not be what assigns IDs).
func TestIngress_AllUserMessagesGetMsgID(t *testing.T) {
	assertAllHaveMsgID := func(t *testing.T, msgs []core.AgentMessage) {
		t.Helper()
		for i, m := range msgs {
			if m.MsgID == "" {
				t.Fatalf("message %d (role=%s) has empty MsgID", i, m.Role)
			}
		}
	}

	t.Run("Run", func(t *testing.T) {
		ag := newTestAgent(NewMockProvider(simpleTextResponse("ok")))
		if _, err := ag.Run(context.Background(), "hi"); err != nil {
			t.Fatal(err)
		}
		assertAllHaveMsgID(t, ag.Messages())
	})

	t.Run("Send", func(t *testing.T) {
		ag := newTestAgent(NewMockProvider(
			simpleTextResponse("a"), simpleTextResponse("b")))
		if _, err := ag.Run(context.Background(), "first"); err != nil {
			t.Fatal(err)
		}
		if _, err := ag.Send(context.Background(), "second"); err != nil {
			t.Fatal(err)
		}
		assertAllHaveMsgID(t, ag.Messages())
	})

	t.Run("SendWithCustom", func(t *testing.T) {
		ag := newTestAgent(NewMockProvider(simpleTextResponse("ok")))
		_, err := ag.SendWithCustom(context.Background(), "hi",
			map[string]any{"source": "subagent"})
		if err != nil {
			t.Fatal(err)
		}
		msgs := ag.Messages()
		assertAllHaveMsgID(t, msgs)
		// Custom metadata must survive alongside the assigned MsgID.
		if msgs[0].Custom["source"] != "subagent" {
			t.Fatalf("custom metadata lost: %+v", msgs[0].Custom)
		}
	})

	t.Run("SendWithContent", func(t *testing.T) {
		ag := newTestAgent(NewMockProvider(simpleTextResponse("ok")))
		_, err := ag.SendWithContent(context.Background(),
			[]core.Content{core.TextContent("hi")})
		if err != nil {
			t.Fatal(err)
		}
		assertAllHaveMsgID(t, ag.Messages())
	})

	t.Run("LoadMessages normalizes legacy", func(t *testing.T) {
		ag := newTestAgent(NewMockProvider())
		// Restore a session whose persisted user message predates stable IDs.
		err := ag.LoadMessages([]core.AgentMessage{
			{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("legacy")}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		assertAllHaveMsgID(t, ag.Messages())
	})

	t.Run("LoadState normalizes legacy", func(t *testing.T) {
		ag := newTestAgent(NewMockProvider())
		err := ag.LoadState([]core.AgentMessage{
			{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("legacy")}}},
		}, 1)
		if err != nil {
			t.Fatal(err)
		}
		assertAllHaveMsgID(t, ag.Messages())
	})

	t.Run("AppendMessage", func(t *testing.T) {
		ag := newTestAgent(NewMockProvider())
		err := ag.AppendMessage(core.AgentMessage{
			Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("appended")}},
		})
		if err != nil {
			t.Fatal(err)
		}
		assertAllHaveMsgID(t, ag.Messages())
	})
}

func TestLoop_ToolCallAndResult(t *testing.T) {
	echoTool := core.Tool{
		Name:        "echo",
		Description: "Echoes input",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			text, _ := params["text"].(string)
			return core.TextResult("echo: " + text), nil
		},
	}

	provider := NewMockProvider(
		toolCallResponse("tc-1", "echo", map[string]any{"text": "hello"}),
		simpleTextResponse("Done."),
	)
	ag := newTestAgent(provider, echoTool)

	msgs, err := ag.Run(context.Background(), "Echo hello")
	if err != nil {
		t.Fatal(err)
	}

	// user, assistant(tool_call), tool_result, assistant(text)
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	if msgs[2].Role != "tool_result" {
		t.Fatalf("expected tool_result at index 2, got %s", msgs[2].Role)
	}
	if msgs[2].Content[0].Text != "echo: hello" {
		t.Fatalf("unexpected tool result: %q", msgs[2].Content[0].Text)
	}
}

func TestLoop_MaxTurnsExceeded(t *testing.T) {
	// Provider always returns a tool call → infinite loop if no guardrail.
	// Each call uses a different arg to avoid triggering doom loop detection.
	callCounter := 0
	infiniteTool := func(req core.Request) (<-chan core.AssistantEvent, error) {
		callCounter++
		ch := make(chan core.AssistantEvent, 5)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.ToolCallContent("tc-loop", "noop", map[string]any{"n": callCounter}),
				},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}

	handlers := make([]func(core.Request) (<-chan core.AssistantEvent, error), 20)
	for i := range handlers {
		handlers[i] = infiniteTool
	}

	noopTool := core.Tool{
		Name:       "noop",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			return core.TextResult("ok"), nil
		},
	}

	provider := NewMockProvider(handlers...)
	ag := newTestAgent(provider, noopTool)
	// Override max turns to 3 for the test
	ag.config.MaxTurns = 3
	events := collectEvents(ag)

	_, err := ag.Run(context.Background(), "Loop forever")
	if err == nil {
		t.Fatal("expected max turns error")
	}
	if err.Error() != "max turns exceeded (3)" {
		t.Fatalf("unexpected error: %v", err)
	}

	// Even on error, we should get complete lifecycle events
	if !waitForEvent(events, core.AgentEventEnd, 500*time.Millisecond) {
		t.Fatal("missing agent_end event on error path")
	}
	if !waitForEvent(events, core.AgentEventError, 500*time.Millisecond) {
		t.Fatal("missing agent_error event")
	}
}

func TestLoop_DoomLoopDetection(t *testing.T) {
	// Provider always returns the exact same tool call → doom loop detection fires
	identicalTool := func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 5)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.ToolCallContent("tc-loop", "noop", map[string]any{"x": "same"}),
				},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}

	handlers := make([]func(core.Request) (<-chan core.AssistantEvent, error), 10)
	for i := range handlers {
		handlers[i] = identicalTool
	}

	noopTool := core.Tool{
		Name:       "noop",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			return core.TextResult("ok"), nil
		},
	}

	provider := NewMockProvider(handlers...)
	ag := newTestAgent(provider, noopTool)
	ag.config.MaxTurns = 100 // high limit so doom loop fires first

	_, err := ag.Run(context.Background(), "Loop forever")
	if err == nil {
		t.Fatal("expected doom loop error")
	}
	if !strings.Contains(err.Error(), "doom loop") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// toolCallHandler builds a provider handler that emits exactly the given tool
// calls in one assistant turn.
func toolCallHandler(calls ...core.Content) func(core.Request) (<-chan core.AssistantEvent, error) {
	return func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 5)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role:       "assistant",
				Content:    calls,
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}
}

func readOnlyTool(name string) core.Tool {
	return core.Tool{
		Name:       name,
		Parameters: json.RawMessage(`{"type":"object"}`),
		Effect:     core.EffectReadOnly,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			return core.TextResult("ok"), nil
		},
	}
}

// TestLoop_DoomLoop_IgnoresStatusPolling verifies that repeatedly polling an
// exempt status tool never trips the doom-loop detector.
func TestLoop_DoomLoop_IgnoresStatusPolling(t *testing.T) {
	poll := core.ToolCallContent("tc-poll", "bash_status", map[string]any{"job_id": "j1"})
	handlers := make([]func(core.Request) (<-chan core.AssistantEvent, error), 10)
	for i := range handlers {
		handlers[i] = toolCallHandler(poll)
	}
	provider := NewMockProvider(handlers...)
	ag := newTestAgent(provider, readOnlyTool("bash_status"))
	ag.config.MaxTurns = 8 // lower than the number of polls: run ends by MaxTurns, not doom loop

	_, err := ag.Run(context.Background(), "poll forever")
	if err == nil {
		t.Fatal("expected an error (MaxTurns), got nil")
	}
	if strings.Contains(err.Error(), "doom loop") {
		t.Fatalf("status polling should not trip doom loop, got: %v", err)
	}
}

// TestLoop_DoomLoop_MixedStatusAndEdit verifies a real repeated edit still trips
// the detector even when identical status polls are interleaved in each turn.
func TestLoop_DoomLoop_MixedStatusAndEdit(t *testing.T) {
	poll := core.ToolCallContent("tc-poll", "bash_status", map[string]any{"job_id": "j1"})
	edit := core.ToolCallContent("tc-edit", "noop", map[string]any{"x": "same"})
	handlers := make([]func(core.Request) (<-chan core.AssistantEvent, error), 10)
	for i := range handlers {
		handlers[i] = toolCallHandler(poll, edit)
	}
	provider := NewMockProvider(handlers...)
	noopTool := core.Tool{
		Name:       "noop",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			return core.TextResult("ok"), nil
		},
	}
	ag := newTestAgent(provider, readOnlyTool("bash_status"), noopTool)
	ag.config.MaxTurns = 100

	_, err := ag.Run(context.Background(), "edit + poll")
	if err == nil || !strings.Contains(err.Error(), "doom loop") {
		t.Fatalf("expected doom loop despite interleaved polling, got: %v", err)
	}
}

// TestLoop_DoomLoop_InterleavedPollingDoesNotReset verifies exempt-only turns
// between identical edits do not reset the streak: edit, poll, edit, poll, edit
// must still trip on the third edit.
func TestLoop_DoomLoop_InterleavedPollingDoesNotReset(t *testing.T) {
	poll := toolCallHandler(core.ToolCallContent("tc-poll", "bash_status", map[string]any{"job_id": "j1"}))
	edit := toolCallHandler(core.ToolCallContent("tc-edit", "noop", map[string]any{"x": "same"}))
	handlers := []func(core.Request) (<-chan core.AssistantEvent, error){
		edit, poll, edit, poll, edit, poll, edit,
	}
	provider := NewMockProvider(handlers...)
	noopTool := core.Tool{
		Name:       "noop",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			return core.TextResult("ok"), nil
		},
	}
	ag := newTestAgent(provider, readOnlyTool("bash_status"), noopTool)
	ag.config.MaxTurns = 100

	_, err := ag.Run(context.Background(), "edit/poll interleaved")
	if err == nil || !strings.Contains(err.Error(), "doom loop") {
		t.Fatalf("interleaved polling must not reset streak, expected doom loop, got: %v", err)
	}
}

func TestLoop_ContextCancellation(t *testing.T) {
	// Provider that blocks until context is cancelled
	slowProvider := func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 5)
		go func() {
			defer close(ch)
			// Block forever
			select {}
		}()
		return ch, nil
	}

	provider := NewMockProvider(slowProvider)
	ag := newTestAgent(provider)

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel after 50ms
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := ag.Run(ctx, "Will be cancelled")
	if err == nil {
		t.Fatal("expected error from cancellation")
	}
}

func TestAbort_DiscardsQueuedSteer(t *testing.T) {
	// A steer queued for a run that is then aborted must NOT survive into the
	// next run. Before the fix it stayed in the buffered channel and got
	// injected as a stale user turn on the following Send.
	providerCalled := make(chan struct{})
	var once sync.Once
	slow := func(req core.Request) (<-chan core.AssistantEvent, error) {
		once.Do(func() { close(providerCalled) })
		ch := make(chan core.AssistantEvent, 1)
		go func() { defer close(ch); select {} }() // block until ctx cancelled
		return ch, nil
	}
	provider := NewMockProvider(slow, simpleTextResponse("second-run reply"))
	ag := newTestAgent(provider)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); ag.Run(ctx, "first") }() //nolint: errcheck

	<-providerCalled
	ag.Steer("stale steer that should be discarded")
	cancel()
	<-done

	msgs, err := ag.Send(context.Background(), "second")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		for _, c := range m.Content {
			if strings.Contains(c.Text, "stale steer") {
				t.Fatalf("stale steer leaked into next run: %+v", msgs)
			}
		}
	}
}

func TestAgent_CancelSteer_DrainsQueuedSteers(t *testing.T) {
	ag := newTestAgent(NewMockProvider())

	ag.Steer("uno")
	ag.Steer("dos")
	if len(ag.steerCh) != 2 {
		t.Fatalf("expected 2 queued steers, got %d", len(ag.steerCh))
	}

	ag.CancelSteer()
	if len(ag.steerCh) != 0 {
		t.Fatalf("expected 0 queued steers after CancelSteer, got %d", len(ag.steerCh))
	}
}

func TestLoop_ToolCallBlocked(t *testing.T) {
	bashTool := core.Tool{
		Name:       "bash",
		Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			t.Fatal("bash should not be executed — it was blocked")
			return core.Result{}, nil
		},
	}

	blocker := &testExtension{initFunc: func(api extension.API) error {
		api.OnToolCall(func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			if name == "bash" {
				return &core.ToolCallDecision{Block: true, Reason: "no shell access"}
			}
			return nil
		})
		return nil
	}}

	provider := NewMockProvider(
		toolCallResponse("tc-bash", "bash", map[string]any{"command": "rm -rf /"}),
		simpleTextResponse("OK, I won't use bash."),
	)

	reg := core.NewRegistry()
	_ = reg.Register(bashTool)
	ag, err := New(AgentConfig{
		Provider:            provider,
		Model:               core.Model{ID: "test"},
		Tools:               reg,
		Extensions:          []extension.Extension{blocker},
		MaxTurns:            10,
		MaxToolCallsPerTurn: 5,
		MaxRunDuration:      30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(ag)

	msgs, err := ag.Run(context.Background(), "Delete everything")
	if err != nil {
		t.Fatal(err)
	}

	// user, assistant(tool_call), tool_result(blocked), assistant(text)
	if len(msgs) < 3 {
		t.Fatalf("expected at least 3 messages, got %d", len(msgs))
	}
	toolResult := msgs[2]
	if !toolResult.IsError {
		t.Fatal("expected tool result to be an error (blocked)")
	}
	if toolResult.Custom != nil && toolResult.Custom["rejected"] == true {
		t.Fatal("non-permission block must not be marked rejected")
	}

	if !waitForEvent(events, core.AgentEventEnd, 2*time.Second) {
		t.Fatal("missing agent_end event")
	}
	var toolEnd *core.AgentEvent
	for _, e := range events.snapshot() {
		if e.Type == core.AgentEventToolExecEnd {
			evt := e
			toolEnd = &evt
		}
	}
	if toolEnd == nil {
		t.Fatal("missing tool_execution_end event")
	}
	if toolEnd.Rejected {
		t.Fatal("non-permission block must emit rejected=false")
	}
}

func TestLoop_ToolPermissionDenied_MarksRejected(t *testing.T) {
	bashTool := core.Tool{
		Name:       "bash",
		Parameters: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			t.Fatal("bash should not be executed — permission denied")
			return core.Result{}, nil
		},
	}

	provider := NewMockProvider(
		toolCallResponse("tc-bash", "bash", map[string]any{"command": "rm -rf /"}),
		simpleTextResponse("OK, I won't use bash."),
	)

	reg := core.NewRegistry()
	_ = reg.Register(bashTool)
	ag, err := New(AgentConfig{
		Provider:            provider,
		Model:               core.Model{ID: "test"},
		Tools:               reg,
		MaxTurns:            10,
		MaxToolCallsPerTurn: 5,
		MaxRunDuration:      30 * time.Second,
		PermissionCheck: func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			if name == "bash" {
				return &core.ToolCallDecision{Block: true, Reason: "not allowed"}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(ag)

	msgs, err := ag.Run(context.Background(), "Delete everything")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) < 3 {
		t.Fatalf("expected at least 3 messages, got %d", len(msgs))
	}
	toolResult := msgs[2]
	if !toolResult.IsError {
		t.Fatal("expected tool result to be an error")
	}
	if toolResult.Custom == nil || toolResult.Custom["rejected"] != true {
		t.Fatalf("expected tool_result custom.rejected=true, got %+v", toolResult.Custom)
	}

	if !waitForEvent(events, core.AgentEventEnd, 2*time.Second) {
		t.Fatal("missing agent_end event")
	}
	var toolEnd *core.AgentEvent
	for _, e := range events.snapshot() {
		if e.Type == core.AgentEventToolExecEnd {
			evt := e
			toolEnd = &evt
		}
	}
	if toolEnd == nil {
		t.Fatal("missing tool_execution_end event")
	}
	if !toolEnd.Rejected {
		t.Fatal("expected tool_execution_end rejected=true for permission denial")
	}
}

func TestLoop_ToolParamValidationFail(t *testing.T) {
	strictTool := core.Tool{
		Name:       "strict",
		Parameters: json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			t.Fatal("should not execute — params are invalid")
			return core.Result{}, nil
		},
	}

	provider := NewMockProvider(
		// Tool call with missing required "count"
		toolCallResponse("tc-strict", "strict", map[string]any{}),
		simpleTextResponse("I see the error."),
	)
	ag := newTestAgent(provider, strictTool)

	msgs, err := ag.Run(context.Background(), "Call strict without count")
	if err != nil {
		t.Fatal(err)
	}

	// Should have validation error in tool_result
	if len(msgs) < 3 {
		t.Fatalf("expected at least 3 messages, got %d", len(msgs))
	}
	toolResult := msgs[2]
	if toolResult.Role != "tool_result" || !toolResult.IsError {
		t.Fatalf("expected error tool_result, got %+v", toolResult.Message)
	}
}

func TestLoop_ExtensionInjectsMessage(t *testing.T) {
	injector := &testExtension{initFunc: func(api extension.API) error {
		api.OnBeforeAgentStart(func(ctx context.Context) ([]core.AgentMessage, error) {
			return []core.AgentMessage{
				core.WrapMessage(core.NewUserMessage("System context: be brief")),
			}, nil
		})
		return nil
	}}

	provider := NewMockProvider(simpleTextResponse("OK."))

	reg := core.NewRegistry()
	ag, err := New(AgentConfig{
		Provider:       provider,
		Model:          core.Model{ID: "test"},
		Tools:          reg,
		Extensions:     []extension.Extension{injector},
		MaxTurns:       10,
		MaxRunDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	msgs, err := ag.Run(context.Background(), "Hello")
	if err != nil {
		t.Fatal(err)
	}

	// user(original), user(injected), assistant
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[1].Content[0].Text != "System context: be brief" {
		t.Fatalf("expected injected message: %q", msgs[1].Content[0].Text)
	}
}

func TestLoop_ObserverHooksFire(t *testing.T) {
	var turnStartCalled, agentEndCalled sync.WaitGroup
	turnStartCalled.Add(1)
	agentEndCalled.Add(1)

	observer := &testExtension{initFunc: func(api extension.API) error {
		api.OnTurnStart(func(ctx context.Context, event core.AgentEvent) {
			turnStartCalled.Done()
		})
		api.OnAgentEnd(func(ctx context.Context, event core.AgentEvent) {
			agentEndCalled.Done()
		})
		return nil
	}}

	provider := NewMockProvider(simpleTextResponse("Hello"))

	reg := core.NewRegistry()
	ag, err := New(AgentConfig{
		Provider:       provider,
		Model:          core.Model{ID: "test"},
		Tools:          reg,
		Extensions:     []extension.Extension{observer},
		MaxTurns:       10,
		MaxRunDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = ag.Run(context.Background(), "Hello")
	if err != nil {
		t.Fatal(err)
	}

	// Wait for async observers with timeout
	done := make(chan struct{})
	go func() {
		turnStartCalled.Wait()
		agentEndCalled.Wait()
		close(done)
	}()
	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("observers not called within timeout")
	}
}

func TestLoop_ErrorMidTurn_ClosesCleanly(t *testing.T) {
	// Provider returns an error during streaming
	errorProvider := func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 5)
		go func() {
			defer close(ch)
			ch <- core.AssistantEvent{
				Type:  core.ProviderEventError,
				Error: fmt.Errorf("simulated stream error"),
			}
		}()
		return ch, nil
	}

	provider := NewMockProvider(errorProvider)
	ag := newTestAgent(provider)
	events := collectEvents(ag)

	_, err := ag.Run(context.Background(), "Trigger error")
	if err == nil {
		t.Fatal("expected error")
	}

	// Verify lifecycle: turn_start, turn_end, agent_error, agent_end
	if !waitForEvent(events, core.AgentEventTurnStart, 500*time.Millisecond) {
		t.Fatal("missing turn_start")
	}
	if !waitForEvent(events, core.AgentEventTurnEnd, 500*time.Millisecond) {
		t.Fatal("missing turn_end (should close on error)")
	}
	if !waitForEvent(events, core.AgentEventError, 500*time.Millisecond) {
		t.Fatal("missing agent_error")
	}
	if !waitForEvent(events, core.AgentEventEnd, 500*time.Millisecond) {
		t.Fatal("missing agent_end")
	}
}

func TestNew_NilProviderError(t *testing.T) {
	_, err := New(AgentConfig{})
	if err == nil {
		t.Fatal("expected error for nil Provider")
	}
	if err.Error() != "agent: Provider is required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoop_MaxToolCallsPerTurn_SkippedResults(t *testing.T) {
	// Provider returns 4 tool calls in one message
	fourToolCalls := func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 5)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.ToolCallContent("tc-1", "echo", map[string]any{"text": "a"}),
					core.ToolCallContent("tc-2", "echo", map[string]any{"text": "b"}),
					core.ToolCallContent("tc-3", "echo", map[string]any{"text": "c"}),
					core.ToolCallContent("tc-4", "echo", map[string]any{"text": "d"}),
				},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}

	echoTool := core.Tool{
		Name:        "echo",
		Description: "Echoes input",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			text, _ := params["text"].(string)
			return core.TextResult("echo: " + text), nil
		},
	}

	provider := NewMockProvider(fourToolCalls, simpleTextResponse("Done."))

	reg := core.NewRegistry()
	_ = reg.Register(echoTool)
	ag, err := New(AgentConfig{
		Provider:            provider,
		Model:               core.Model{ID: "test"},
		Tools:               reg,
		MaxTurns:            10,
		MaxToolCallsPerTurn: 2,
		MaxRunDuration:      30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	msgs, err := ag.Run(context.Background(), "Do four things")
	if err != nil {
		t.Fatal(err)
	}

	// Count tool results
	var toolResults []core.AgentMessage
	for _, m := range msgs {
		if m.Role == "tool_result" {
			toolResults = append(toolResults, m)
		}
	}

	// All 4 tool calls should have corresponding results
	if len(toolResults) != 4 {
		t.Fatalf("expected 4 tool_result messages, got %d", len(toolResults))
	}

	// First 2 should be normal, last 2 should be skipped/error
	for i, tr := range toolResults {
		if i < 2 {
			if tr.IsError {
				t.Errorf("tool result %d should not be error", i)
			}
		} else {
			if !tr.IsError {
				t.Errorf("tool result %d should be error (skipped)", i)
			}
		}
	}
}

func TestParallelToolCalls_ConcurrentExecution(t *testing.T) {
	// 3 tool calls that each sleep 100ms. If parallel, total < 250ms.
	// If sequential, total ≥ 300ms.
	sleepTool := core.Tool{
		Name:        "slow",
		Description: "Sleeps briefly",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		Effect:      core.EffectReadOnly,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			time.Sleep(100 * time.Millisecond)
			id, _ := params["id"].(string)
			return core.TextResult("done-" + id), nil
		},
	}

	threeToolCalls := func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 5)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.ToolCallContent("tc-1", "slow", map[string]any{"id": "a"}),
					core.ToolCallContent("tc-2", "slow", map[string]any{"id": "b"}),
					core.ToolCallContent("tc-3", "slow", map[string]any{"id": "c"}),
				},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}

	provider := NewMockProvider(threeToolCalls, simpleTextResponse("All done."))
	reg := core.NewRegistry()
	_ = reg.Register(sleepTool)
	ag, err := New(AgentConfig{
		Provider:       provider,
		Model:          core.Model{ID: "test"},
		Tools:          reg,
		MaxTurns:       10,
		MaxRunDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	msgs, err := ag.Run(context.Background(), "Do three things")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}

	// Verify concurrency: 3 × 100ms should complete in < 250ms if parallel.
	if elapsed >= 250*time.Millisecond {
		t.Fatalf("expected parallel execution (< 250ms), took %v", elapsed)
	}

	// Verify result ordering matches tool call ordering.
	var results []string
	for _, m := range msgs {
		if m.Role == "tool_result" {
			for _, c := range m.Content {
				if c.Type == "text" {
					results = append(results, c.Text)
				}
			}
		}
	}
	want := []string{"done-a", "done-b", "done-c"}
	if len(results) != len(want) {
		t.Fatalf("expected %d results, got %d", len(want), len(results))
	}
	for i, r := range results {
		if r != want[i] {
			t.Errorf("result[%d] = %q, want %q", i, r, want[i])
		}
	}
}

func TestParallelToolCalls_SingleCallRegression(t *testing.T) {
	echoTool := core.Tool{
		Name:        "echo",
		Description: "Echoes",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			text, _ := params["text"].(string)
			return core.TextResult("echo: " + text), nil
		},
	}

	provider := NewMockProvider(
		toolCallResponse("tc-1", "echo", map[string]any{"text": "hello"}),
		simpleTextResponse("Done."),
	)
	ag := newTestAgent(provider, echoTool)

	msgs, err := ag.Run(context.Background(), "Echo something")
	if err != nil {
		t.Fatal(err)
	}

	var toolResults []core.AgentMessage
	for _, m := range msgs {
		if m.Role == "tool_result" {
			toolResults = append(toolResults, m)
		}
	}
	if len(toolResults) != 1 {
		t.Fatalf("expected 1 tool_result, got %d", len(toolResults))
	}
	if toolResults[0].IsError {
		t.Fatal("tool result should not be error")
	}
}

func TestParallelToolCalls_EventOrder(t *testing.T) {
	// Verify that tool_execution_start events come before tool_execution_end,
	// and that all ends are emitted in order even with concurrent execution.
	sleepTool := core.Tool{
		Name:        "slow",
		Description: "Sleeps",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			time.Sleep(50 * time.Millisecond)
			id, _ := params["id"].(string)
			return core.TextResult("done-" + id), nil
		},
	}

	twoToolCalls := func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 5)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.ToolCallContent("tc-1", "slow", map[string]any{"id": "a"}),
					core.ToolCallContent("tc-2", "slow", map[string]any{"id": "b"}),
				},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}

	provider := NewMockProvider(twoToolCalls, simpleTextResponse("Done."))
	reg := core.NewRegistry()
	_ = reg.Register(sleepTool)
	ag, err := New(AgentConfig{
		Provider:       provider,
		Model:          core.Model{ID: "test"},
		Tools:          reg,
		MaxTurns:       10,
		MaxRunDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	collector := collectEvents(ag)
	_, err = ag.Run(context.Background(), "Do two things")
	if err != nil {
		t.Fatal(err)
	}

	// Wait for events to propagate.
	if !waitForEvent(collector, core.AgentEventEnd, 2*time.Second) {
		t.Fatal("timeout waiting for agent_end")
	}

	events := collector.snapshot()

	// All starts must come before all ends (since starts are emitted before
	// concurrent execution, and ends are emitted sequentially after).
	var starts, ends []string
	lastStartIdx := -1
	firstEndIdx := len(events)
	for i, e := range events {
		switch e.Type {
		case core.AgentEventToolExecStart:
			starts = append(starts, e.ToolCallID)
			lastStartIdx = i
		case core.AgentEventToolExecEnd:
			ends = append(ends, e.ToolCallID)
			if i < firstEndIdx {
				firstEndIdx = i
			}
		}
	}

	if len(starts) != 2 || len(ends) != 2 {
		t.Fatalf("expected 2 starts + 2 ends, got %d starts + %d ends", len(starts), len(ends))
	}
	if lastStartIdx >= firstEndIdx {
		t.Fatal("expected all tool_execution_start events before any tool_execution_end")
	}
	// Ends must be in order (tc-1, tc-2) since Phase 3 is sequential.
	if ends[0] != "tc-1" || ends[1] != "tc-2" {
		t.Fatalf("tool_execution_end order = %v, want [tc-1, tc-2]", ends)
	}
}

func TestParallelToolCalls_ContextCancellation(t *testing.T) {
	// A tool that blocks until context is cancelled.
	blockTool := core.Tool{
		Name:        "block",
		Description: "Blocks",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			<-ctx.Done()
			return core.ErrorResult(ctx.Err().Error()), ctx.Err()
		},
	}

	twoToolCalls := func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 5)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.ToolCallContent("tc-1", "block", nil),
					core.ToolCallContent("tc-2", "block", nil),
				},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}

	provider := NewMockProvider(twoToolCalls)
	reg := core.NewRegistry()
	_ = reg.Register(blockTool)
	ag, err := New(AgentConfig{
		Provider:       provider,
		Model:          core.Model{ID: "test"},
		Tools:          reg,
		MaxTurns:       10,
		MaxRunDuration: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = ag.Run(context.Background(), "Block forever")
	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
}

func TestLoop_ToolReturnsErrorResult(t *testing.T) {
	failTool := core.Tool{
		Name:        "fail",
		Description: "Always fails",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			return core.ErrorResult("file not found"), nil
		},
	}

	provider := NewMockProvider(
		toolCallResponse("tc-fail", "fail", map[string]any{}),
		simpleTextResponse("I see the error."),
	)
	ag := newTestAgent(provider, failTool)

	msgs, err := ag.Run(context.Background(), "Try to fail")
	if err != nil {
		t.Fatal(err)
	}

	// user, assistant(tool_call), tool_result, assistant(text)
	if len(msgs) < 3 {
		t.Fatalf("expected at least 3 messages, got %d", len(msgs))
	}
	toolResult := msgs[2]
	if toolResult.Role != "tool_result" {
		t.Fatalf("expected tool_result, got %s", toolResult.Role)
	}
	if !toolResult.IsError {
		t.Fatal("expected IsError=true for ErrorResult tool")
	}
}

// --- Multi-turn tests ---

func TestSend_MultiTurn(t *testing.T) {
	provider := NewMockProvider(
		simpleTextResponse("Hello!"),
		simpleTextResponse("I remember!"),
	)
	ag := newTestAgent(provider)

	msgs, err := ag.Send(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after first Send, got %d", len(msgs))
	}

	msgs, err = ag.Send(context.Background(), "do you remember?")
	if err != nil {
		t.Fatal(err)
	}
	// Should accumulate: user, assistant, user, assistant
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages after second Send, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" ||
		msgs[2].Role != "user" || msgs[3].Role != "assistant" {
		t.Fatalf("unexpected roles: %s, %s, %s, %s",
			msgs[0].Role, msgs[1].Role, msgs[2].Role, msgs[3].Role)
	}
	if msgs[2].Content[0].Text != "do you remember?" {
		t.Fatalf("unexpected second user message: %q", msgs[2].Content[0].Text)
	}
	if msgs[3].Content[0].Text != "I remember!" {
		t.Fatalf("unexpected second assistant message: %q", msgs[3].Content[0].Text)
	}
}

func TestSend_AutoInit(t *testing.T) {
	provider := NewMockProvider(simpleTextResponse("Hi there!"))
	ag := newTestAgent(provider)

	// Send without Run — should auto-initialize
	msgs, err := ag.Send(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("unexpected roles: %s, %s", msgs[0].Role, msgs[1].Role)
	}
}

func TestReset_ClearsState(t *testing.T) {
	provider := NewMockProvider(
		simpleTextResponse("First response"),
		simpleTextResponse("Fresh response"),
	)
	ag := newTestAgent(provider)

	_, err := ag.Send(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}

	if err := ag.Reset(); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	msgs, err := ag.Send(context.Background(), "fresh start")
	if err != nil {
		t.Fatal(err)
	}
	// Should only have 2 messages (history was cleared)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after Reset+Send, got %d", len(msgs))
	}
	if msgs[0].Content[0].Text != "fresh start" {
		t.Fatalf("expected 'fresh start', got %q", msgs[0].Content[0].Text)
	}
}

func TestReset_WhileRunning_ReturnsError(t *testing.T) {
	// Provider that blocks forever (until test cancels context)
	blockingProvider := func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 1)
		go func() {
			defer close(ch)
			select {} // block forever
		}()
		return ch, nil
	}

	provider := NewMockProvider(blockingProvider)
	ag := newTestAgent(provider)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start Send in background
	done := make(chan struct{})
	go func() {
		defer close(done)
		ag.Send(ctx, "will block") //nolint: errcheck
	}()

	// Poll until agent is running
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := ag.Reset()
		if err != nil && err.Error() == "cannot reset while agent is running" {
			// Success — Reset correctly detected running state
			cancel() // cleanup
			<-done
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("Reset never returned 'cannot reset while agent is running'")
}

func TestSetCompactAt_StoresValue(t *testing.T) {
	ag := newTestAgent(NewMockProvider(simpleTextResponse("ok")))

	if err := ag.SetCompactAt(260_000); err != nil {
		t.Fatalf("SetCompactAt failed: %v", err)
	}
	if got := ag.config.Compaction.CompactAt; got != 260_000 {
		t.Fatalf("expected CompactAt=260000, got %d", got)
	}
	// 0 restores default (window-based) behavior.
	if err := ag.SetCompactAt(0); err != nil {
		t.Fatalf("SetCompactAt(0) failed: %v", err)
	}
	if got := ag.config.Compaction.CompactAt; got != 0 {
		t.Fatalf("expected CompactAt=0, got %d", got)
	}
}

func TestSetCompactAt_CopyOnWrite(t *testing.T) {
	// A shared settings pointer must not be mutated in place.
	shared := core.DefaultCompactionSettings
	ag := newTestAgent(NewMockProvider(simpleTextResponse("ok")))
	ag.config.Compaction = &shared

	if err := ag.SetCompactAt(123_456); err != nil {
		t.Fatalf("SetCompactAt failed: %v", err)
	}
	if shared.CompactAt != 0 {
		t.Fatal("shared settings were mutated in place; expected copy-on-write")
	}
	if ag.config.Compaction.CompactAt != 123_456 {
		t.Fatal("agent settings did not pick up the new value")
	}
}

func TestSetCompactAt_WhileRunning_ReturnsError(t *testing.T) {
	blockingProvider := func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 1)
		go func() {
			defer close(ch)
			select {} // block forever
		}()
		return ch, nil
	}
	ag := newTestAgent(NewMockProvider(blockingProvider))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		ag.Send(ctx, "will block") //nolint: errcheck
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := ag.SetCompactAt(260_000); err != nil {
			cancel()
			<-done
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("SetCompactAt never rejected while running")
}

func TestSend_WhileRunning_ReturnsError(t *testing.T) {
	// Use a signal channel so we know the provider was called (agent is running).
	providerCalled := make(chan struct{})
	blockingProvider := func(req core.Request) (<-chan core.AssistantEvent, error) {
		close(providerCalled) // signal: agent is definitely running
		ch := make(chan core.AssistantEvent, 1)
		go func() {
			defer close(ch)
			select {} // block forever
		}()
		return ch, nil
	}

	provider := NewMockProvider(blockingProvider)
	ag := newTestAgent(provider)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start first Send in background
	done := make(chan struct{})
	go func() {
		defer close(done)
		ag.Send(ctx, "will block") //nolint: errcheck
	}()

	// Wait until provider is called — agent is definitely running
	select {
	case <-providerCalled:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("provider was never called")
	}

	// Now try concurrent Send — must be rejected
	_, err := ag.Send(context.Background(), "concurrent")
	if err == nil || err.Error() != "agent is already running" {
		cancel()
		<-done
		t.Fatalf("expected 'agent is already running', got: %v", err)
	}

	cancel()
	<-done
}

func TestCompact_SerializesAgainstRun(t *testing.T) {
	// Regression for the Compact()-vs-run race: Compact used to release the
	// mutex during the (seconds-long) summarization LLM call without claiming
	// the running slot, so a concurrent Send() would start a run whose appended
	// messages got stomped by the stale pre-compaction snapshot. Now Compact
	// holds the slot for its whole duration, so a concurrent run is refused.
	started := make(chan struct{})
	release := make(chan struct{})
	blocking := func(req core.Request) (<-chan core.AssistantEvent, error) {
		close(started) // compaction has reached the LLM call
		<-release      // hold the slot open
		return simpleTextResponse("SUMMARY")(req)
	}

	ag, err := New(AgentConfig{
		Provider:            NewMockProvider(blocking),
		Model:               core.Model{ID: "test-model", Provider: "mock", MaxInput: 100},
		SystemPrompt:        "test",
		Tools:               core.NewRegistry(),
		MaxTurns:            10,
		MaxToolCallsPerTurn: 5,
		MaxRunDuration:      30 * time.Second,
		Compaction:          &core.CompactionSettings{Enabled: true, ReserveTokens: 10, KeepRecent: 10},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Seed enough messages to force a cut point > 0 so Compact reaches Stream.
	for i := 0; i < 30; i++ {
		ag.state.Messages = append(ag.state.Messages,
			core.WrapMessage(core.NewUserMessage(fmt.Sprintf("message number %d", i))))
	}

	compactErr := make(chan error, 1)
	go func() { _, e := ag.Compact(context.Background()); compactErr <- e }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction never reached the provider")
	}

	// While compaction holds the slot, a run must be refused — not corrupt state.
	if _, err := ag.Send(context.Background(), "concurrent"); err == nil ||
		err.Error() != "agent is already running" {
		close(release)
		<-compactErr
		t.Fatalf("expected 'agent is already running' during compaction, got: %v", err)
	}

	close(release)
	if err := <-compactErr; err != nil {
		t.Fatalf("compaction failed: %v", err)
	}
	// The compacted transcript replaced the seeded messages; no orphan/stomp.
	if got := len(ag.state.Messages); got == 0 {
		t.Fatal("compaction produced no messages")
	}
}

// TestCompact_RetainedUserKeepsStableMsgID is the faithful integration guard for
// bug #13. It drives real ingress (Send) followed by a real Compact() and checks
// that a user message retained across compaction keeps the SAME, non-empty MsgID
// it had once synced. The bug: Send inserted the user without a MsgID, so the
// tree syncer first recorded it under a positional "legacy:<index>" identity,
// then Compact's EnsureMsgID minted a fresh MsgID for the still-anonymous state
// copy — a different id than the tree held — and the next sync re-appended it as
// a duplicate after the compaction marker. With ingress normalization, the id is
// assigned at Send time and Compact preserves it (EnsureMsgID is idempotent).
func TestCompact_RetainedUserKeepsStableMsgID(t *testing.T) {
	msgText := func(m core.AgentMessage) string {
		if len(m.Content) == 0 {
			return ""
		}
		return m.Content[0].Text
	}
	// Provider answers every call (each Send runs the loop; plus the summary).
	// Auto-compaction is DISABLED (Enabled:false) so the seeded user messages
	// stay in state exactly as ingress left them — otherwise a mid-seed
	// auto-compaction would normalize their ids and mask the bug. The manual
	// Compact() below ignores Enabled and still cuts.
	ag, err := New(AgentConfig{
		Provider:            &alwaysProvider{text: "ok"},
		Model:               core.Model{ID: "test-model", Provider: "mock", MaxInput: 100},
		SystemPrompt:        "test",
		Tools:               core.NewRegistry(),
		MaxTurns:            10,
		MaxToolCallsPerTurn: 5,
		MaxRunDuration:      30 * time.Second,
		Compaction:          &core.CompactionSettings{Enabled: false, ReserveTokens: 10, KeepRecent: 10},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Seed a long history of user turns via the real ingress path, then mark the
	// LAST one (which will be retained past the cut point) with a sentinel we can
	// find after compaction. Simulate the "already synced" turns having assistant
	// replies so the cut lands on a clean boundary.
	for i := 0; i < 30; i++ {
		if _, e := ag.SendWithContent(context.Background(),
			[]core.Content{core.TextContent(fmt.Sprintf("message number %d", i))}); e != nil {
			t.Fatal(e)
		}
	}

	// Capture the retained tail's user MsgIDs as ingress assigned them.
	before := map[string]string{} // text -> MsgID
	for _, m := range ag.Messages() {
		if m.Role == "user" && m.MsgID == "" {
			t.Fatalf("ingress left a user message without MsgID: %q", msgText(m))
		}
		if m.Role == "user" {
			before[msgText(m)] = m.MsgID
		}
	}

	if _, err := ag.Compact(context.Background()); err != nil {
		t.Fatalf("compaction failed: %v", err)
	}

	// Every retained user message must keep the exact MsgID it had before the
	// compaction. A changed id is what the syncer would treat as a new message.
	for _, m := range ag.Messages() {
		if m.Role != "user" {
			continue
		}
		txt := msgText(m)
		prev, ok := before[txt]
		if !ok {
			continue // summarized away
		}
		if m.MsgID == "" {
			t.Fatalf("retained user %q lost its MsgID after compaction", txt)
		}
		if m.MsgID != prev {
			t.Fatalf("retained user %q changed MsgID across compaction: %q -> %q (would duplicate on next sync)", txt, prev, m.MsgID)
		}
	}
}

func TestRun_AfterSend_Resets(t *testing.T) {
	provider := NewMockProvider(
		simpleTextResponse("Send response"),
		simpleTextResponse("Run response"),
	)
	ag := newTestAgent(provider)

	_, err := ag.Send(context.Background(), "hello via send")
	if err != nil {
		t.Fatal(err)
	}

	// Run should reset state
	msgs, err := ag.Run(context.Background(), "fresh via run")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after Run (reset), got %d", len(msgs))
	}
	if msgs[0].Content[0].Text != "fresh via run" {
		t.Fatalf("expected 'fresh via run', got %q", msgs[0].Content[0].Text)
	}
}

// --- LoadMessages / Messages ---

func TestLoadMessages(t *testing.T) {
	prov := NewMockProvider(simpleTextResponse("hello"))
	ag, err := New(AgentConfig{Provider: prov, Model: core.Model{ID: "test"}})
	if err != nil {
		t.Fatal(err)
	}

	msgs := []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("previous question")),
		{Message: core.Message{
			Role:    "assistant",
			Content: []core.Content{{Type: "text", Text: "previous answer"}},
		}},
	}

	if err := ag.LoadMessages(msgs); err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}

	// Messages() should return a copy
	got := ag.Messages()
	if len(got) != 2 {
		t.Fatalf("Messages() = %d, want 2", len(got))
	}
	if got[0].Content[0].Text != "previous question" {
		t.Errorf("Messages()[0] = %q, want 'previous question'", got[0].Content[0].Text)
	}

	// Appending to the returned slice should not affect internal state
	got2 := ag.Messages()
	if len(got2) != 2 {
		t.Error("Messages() should return an independent slice (append-safe)")
	}
}

func TestLoadMessages_WhileRunning(t *testing.T) {
	// Create a provider that blocks until cancelled
	prov := NewMockProvider(func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent)
		// Channel stays open until provider's Stream ctx is cancelled
		return ch, nil
	})

	ag, _ := New(AgentConfig{Provider: prov, Model: core.Model{ID: "test"}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start a run in background
	done := make(chan struct{})
	go func() {
		_, _ = ag.Run(ctx, "block forever")
		close(done)
	}()

	// Give it time to start
	time.Sleep(50 * time.Millisecond)

	// LoadMessages should fail while running
	err := ag.LoadMessages([]core.AgentMessage{})
	if err == nil {
		t.Error("expected error from LoadMessages while running")
	}

	cancel()
	<-done
}

func TestSendAfterLoadMessages(t *testing.T) {
	// First call: simpleTextResponse for the resumed Send
	prov := NewMockProvider(simpleTextResponse("continued"))
	ag, _ := New(AgentConfig{Provider: prov, Model: core.Model{ID: "test"}})

	// Load previous conversation
	if err := ag.LoadMessages([]core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("first")),
		{Message: core.Message{
			Role:    "assistant",
			Content: []core.Content{{Type: "text", Text: "first response"}},
		}},
	}); err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}

	// Send continues the conversation
	msgs, err := ag.Send(context.Background(), "second")
	if err != nil {
		t.Fatal(err)
	}

	// Should have: first user + first assistant + second user + second assistant
	if len(msgs) != 4 {
		t.Fatalf("Messages = %d, want 4", len(msgs))
	}
	if msgs[2].Content[0].Text != "second" {
		t.Errorf("msgs[2] = %q, want 'second'", msgs[2].Content[0].Text)
	}
	if msgs[3].Content[0].Text != "continued" {
		t.Errorf("msgs[3] = %q, want 'continued'", msgs[3].Content[0].Text)
	}
}

// --- Compaction tests ---

// largeTextResponse returns a handler that streams a large text response
// with a given Usage to simulate a big context.
func largeTextResponse(text string, usage *core.Usage) func(req core.Request) (<-chan core.AssistantEvent, error) {
	return func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role:       "assistant",
				Content:    []core.Content{core.TextContent(text)},
				StopReason: "end_turn",
				Timestamp:  time.Now().Unix(),
				Usage:      usage,
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, ContentIndex: 0, Delta: text}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}
}

func TestLoop_CompactionTriggered(t *testing.T) {
	// Use a small context window (1000 tokens) so small messages trigger compaction.
	// The large response (~500 tokens text) fills the window quickly.
	bigText := strings.Repeat("x", 2000) // 2000 chars ≈ 500 tokens

	prov := NewMockProvider(
		// Turn 1: large response.
		largeTextResponse(bigText, &core.Usage{TotalTokens: 900}),
		// Summarization call from compaction.
		simpleTextResponse("## Goal\nTest compaction"),
		// Turn 2: normal response after compaction.
		simpleTextResponse("second response"),
	)

	reg := core.NewRegistry()
	settings := core.CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecent: 200}
	ag, err := New(AgentConfig{
		Provider:            prov,
		Model:               core.Model{ID: "test", MaxInput: 1000},
		Compaction:          &settings,
		Tools:               reg,
		MaxTurns:            10,
		MaxToolCallsPerTurn: 5,
		MaxRunDuration:      30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Track compaction events.
	var compactionEvents []core.AgentEvent
	var mu sync.Mutex
	ag.Subscribe(func(e core.AgentEvent) {
		if e.Type == core.AgentEventCompactionStart || e.Type == core.AgentEventCompactionEnd {
			mu.Lock()
			compactionEvents = append(compactionEvents, e)
			mu.Unlock()
		}
	})

	// Turn 1.
	msgs, err := ag.Run(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(msgs))
	}

	// Turn 2: should trigger compaction before the provider call.
	msgs, err = ag.Send(context.Background(), "continue")
	if err != nil {
		t.Fatal(err)
	}

	// Verify compaction happened: first message should be compaction_summary.
	if len(msgs) == 0 {
		t.Fatal("expected messages")
	}
	if msgs[0].Role != "compaction_summary" {
		t.Fatalf("first message should be compaction_summary, got %s", msgs[0].Role)
	}

	// Verify epoch incremented.
	if ag.CompactionEpoch() != 1 {
		t.Fatalf("expected epoch 1, got %d", ag.CompactionEpoch())
	}

	// Verify compaction events fired (async — poll).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(compactionEvents)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(compactionEvents) < 2 {
		t.Fatalf("expected at least 2 compaction events, got %d", len(compactionEvents))
	}
	if compactionEvents[0].Type != core.AgentEventCompactionStart {
		t.Fatalf("expected compaction_start, got %s", compactionEvents[0].Type)
	}
	if compactionEvents[1].Type != core.AgentEventCompactionEnd {
		t.Fatalf("expected compaction_end, got %s", compactionEvents[1].Type)
	}
	if compactionEvents[1].Compaction == nil {
		t.Fatal("expected CompactionPayload")
	}
}

func TestLoop_CompactionDisabled(t *testing.T) {
	prov := NewMockProvider(
		simpleTextResponse("response"),
	)
	reg := core.NewRegistry()
	disabled := core.CompactionSettings{Enabled: false}
	ag, err := New(AgentConfig{
		Provider:       prov,
		Model:          core.Model{ID: "test", MaxInput: 200_000},
		Compaction:     &disabled,
		Tools:          reg,
		MaxTurns:       10,
		MaxRunDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = ag.Run(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if ag.CompactionEpoch() != 0 {
		t.Fatalf("compaction should not have fired, epoch=%d", ag.CompactionEpoch())
	}
}

func TestDefaultConvertToLLM_CompactionSummary(t *testing.T) {
	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "compaction_summary", Content: []core.Content{core.TextContent("summary text")}}},
		core.WrapMessage(core.NewUserMessage("hello")),
		core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("hi")}}),
	}
	llm := defaultConvertToLLM(msgs)
	if len(llm) != 3 {
		t.Fatalf("expected 3 LLM messages, got %d", len(llm))
	}
	// First should be converted to user role with wrapper.
	if llm[0].Role != "user" {
		t.Fatalf("expected user role for summary, got %s", llm[0].Role)
	}
	text := llm[0].Content[0].Text
	if !contains(text, "<summary>") || !contains(text, "summary text") {
		t.Fatalf("expected wrapped summary, got: %s", text)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestReconfigure_SwapModel(t *testing.T) {
	prov1 := NewMockProvider(simpleTextResponse("from model 1"))
	prov2 := NewMockProvider(simpleTextResponse("from model 2"))

	ag, _ := New(AgentConfig{
		Provider: prov1,
		Model:    core.Model{ID: "model-1", Provider: "prov-a"},
	})

	// First turn with model 1.
	_, err := ag.Run(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}

	// Reconfigure to model 2 (different provider).
	err = ag.Reconfigure(prov2, core.Model{ID: "model-2", Provider: "prov-b"}, "high")
	if err != nil {
		t.Fatal(err)
	}

	// Verify model changed.
	if ag.Model().ID != "model-2" {
		t.Fatalf("expected model-2, got %s", ag.Model().ID)
	}
	if ag.ThinkingLevel() != "high" {
		t.Fatalf("expected thinking high, got %s", ag.ThinkingLevel())
	}

	// Second turn with model 2 — conversation continues.
	msgs, err := ag.Send(context.Background(), "continue")
	if err != nil {
		t.Fatal(err)
	}
	// Should have messages from both turns.
	if len(msgs) < 4 {
		t.Fatalf("expected at least 4 messages, got %d", len(msgs))
	}
}

func TestSetModel_RejectsUnpricedModelWhenBudgetSet(t *testing.T) {
	// New() enforces MaxBudget>0 ⇒ Pricing!=nil; the setters must too, else
	// switching to an unpriced model silently disables the cost guardrail.
	priced := core.Model{ID: "priced", Provider: "mock", Pricing: &core.Pricing{Input: 1, Output: 1}}
	ag, err := New(AgentConfig{
		Provider:  NewMockProvider(simpleTextResponse("ok")),
		Model:     priced,
		MaxBudget: 5,
	})
	if err != nil {
		t.Fatal(err)
	}

	unpriced := core.Model{ID: "unpriced", Provider: "mock"}
	if err := ag.SetModel(nil, unpriced); err == nil {
		t.Error("SetModel to unpriced model should be rejected while MaxBudget is set")
	}
	if err := ag.Reconfigure(nil, unpriced, "medium"); err == nil {
		t.Error("Reconfigure to unpriced model should be rejected while MaxBudget is set")
	}
	if ag.Model().ID != "priced" {
		t.Fatalf("model must be unchanged after rejected switch, got %s", ag.Model().ID)
	}
	// A priced model still switches fine.
	priced2 := core.Model{ID: "priced2", Provider: "mock", Pricing: &core.Pricing{Input: 2, Output: 2}}
	if err := ag.SetModel(nil, priced2); err != nil {
		t.Fatalf("SetModel to priced model should succeed: %v", err)
	}
}

func TestReconfigure_StripsThinking(t *testing.T) {
	prov := NewMockProvider(simpleTextResponse("ok"))
	ag, _ := New(AgentConfig{
		Provider: prov,
		Model:    core.Model{ID: "model-1", Provider: "anthropic"},
	})

	// Manually inject a message with thinking blocks.
	if err := ag.LoadMessages([]core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("hello")),
		core.WrapMessage(core.Message{
			Role: "assistant",
			Content: []core.Content{
				core.ThinkingContent("secret reasoning"),
				core.TextContent("visible response"),
			},
		}),
	}); err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}

	// Reconfigure to a different model.
	err := ag.Reconfigure(nil, core.Model{ID: "model-2", Provider: "anthropic"}, "medium")
	if err != nil {
		t.Fatal(err)
	}

	// Thinking blocks should be stripped.
	msgs := ag.Messages()
	for _, m := range msgs {
		if m.Role == "assistant" {
			for _, c := range m.Content {
				if c.Type == "thinking" {
					t.Fatal("thinking blocks should have been stripped")
				}
			}
			if len(m.Content) != 1 || m.Content[0].Text != "visible response" {
				t.Fatalf("expected only text content, got %+v", m.Content)
			}
		}
	}
}

func TestReconfigure_SameModelKeepsThinking(t *testing.T) {
	prov := NewMockProvider(simpleTextResponse("ok"))
	ag, _ := New(AgentConfig{
		Provider: prov,
		Model:    core.Model{ID: "model-1", Provider: "anthropic"},
	})

	if err := ag.LoadMessages([]core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("hello")),
		core.WrapMessage(core.Message{
			Role: "assistant",
			Content: []core.Content{
				core.ThinkingContent("reasoning"),
				core.TextContent("response"),
			},
		}),
	}); err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}

	// Reconfigure same model, different thinking level — should NOT strip.
	err := ag.Reconfigure(nil, core.Model{ID: "model-1", Provider: "anthropic"}, "high")
	if err != nil {
		t.Fatal(err)
	}

	msgs := ag.Messages()
	hasThinking := false
	for _, m := range msgs {
		for _, c := range m.Content {
			if c.Type == "thinking" {
				hasThinking = true
			}
		}
	}
	if !hasThinking {
		t.Fatal("thinking blocks should be preserved when model doesn't change")
	}
}

func TestReconfigure_WhileRunning(t *testing.T) {
	blocker := make(chan struct{})
	prov := NewMockProvider(func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 5)
		go func() {
			defer close(ch)
			<-blocker
			msg := core.Message{Role: "assistant", Content: []core.Content{core.TextContent("done")}, StopReason: "end_turn"}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	})

	ag, _ := New(AgentConfig{Provider: prov, Model: core.Model{ID: "test"}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _, _ = ag.Run(ctx, "hello") }()
	time.Sleep(50 * time.Millisecond) // let it start

	err := ag.Reconfigure(nil, core.Model{ID: "other"}, "high")
	if err == nil {
		t.Fatal("expected error while running")
	}

	close(blocker)
	time.Sleep(100 * time.Millisecond)
}

func TestLoadState(t *testing.T) {
	prov := NewMockProvider(simpleTextResponse("ok"))
	ag, _ := New(AgentConfig{Provider: prov, Model: core.Model{ID: "test"}})

	msgs := []core.AgentMessage{
		{Message: core.Message{Role: "compaction_summary", Content: []core.Content{core.TextContent("old summary")}}},
		core.WrapMessage(core.NewUserMessage("old msg")),
	}
	if err := ag.LoadState(msgs, 3); err != nil {
		t.Fatal(err)
	}
	if ag.CompactionEpoch() != 3 {
		t.Fatalf("expected epoch 3, got %d", ag.CompactionEpoch())
	}
	loaded := ag.Messages()
	if len(loaded) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(loaded))
	}
}

func TestAppendMessage(t *testing.T) {
	prov := NewMockProvider(simpleTextResponse("ok"))
	ag, _ := New(AgentConfig{Provider: prov, Model: core.Model{ID: "test", Provider: "anthropic"}})

	event := core.AgentMessage{
		Message: core.Message{
			Role:    "session_event",
			Content: []core.Content{core.TextContent("✓ Switched to o3 (openai)")},
		},
		Custom: map[string]any{"event": "model_switch"},
	}
	if err := ag.AppendMessage(event); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	msgs := ag.Messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if msgs[0].Role != "session_event" {
		t.Fatalf("messages[0].Role = %q, want session_event", msgs[0].Role)
	}

	msgs, err := ag.Send(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) < 3 {
		t.Fatalf("messages = %d, want at least 3", len(msgs))
	}
	if msgs[0].Role != "session_event" || msgs[1].Role != "user" {
		t.Fatalf("expected session event before new user message, got roles %q then %q", msgs[0].Role, msgs[1].Role)
	}
}

func TestDefaultConvertToLLM_IgnoresSessionEvent(t *testing.T) {
	msgs := []core.AgentMessage{
		{
			Message: core.Message{Role: "session_event", Content: []core.Content{core.TextContent("✓ Switched to o3 (openai)")}},
			Custom:  map[string]any{"event": "model_switch"},
		},
		core.WrapMessage(core.NewUserMessage("hello")),
	}
	llm := defaultConvertToLLM(msgs)
	if len(llm) != 1 {
		t.Fatalf("expected 1 LLM message, got %d", len(llm))
	}
	if llm[0].Role != "user" || llm[0].Content[0].Text != "hello" {
		t.Fatalf("unexpected LLM messages: %+v", llm)
	}
}

// TestAgent_ConcurrentReadsDuringRun exercises the data race on a.state: the
// loop appends messages during a run while external readers call Messages()/
// CompactionEpoch(). Before the fix, the loop wrote a.state.Messages without
// the lock the readers hold — run with -race to catch it.
func TestAgent_ConcurrentReadsDuringRun(t *testing.T) {
	noopTool := core.Tool{
		Name:       "noop",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			return core.TextResult("ok"), nil
		},
	}

	// A tool-call turn that streams slowly, widening the window in which the
	// loop appends to a.state while readers are active. Args vary per turn so
	// the doom-loop detector does not trip.
	slowToolCall := func(n int) func(req core.Request) (<-chan core.AssistantEvent, error) {
		return func(req core.Request) (<-chan core.AssistantEvent, error) {
			ch := make(chan core.AssistantEvent, 10)
			go func() {
				defer close(ch)
				msg := core.Message{
					Role: "assistant",
					Content: []core.Content{
						core.TextContent("using tool"),
						core.ToolCallContent(fmt.Sprintf("t%d", n), "noop", map[string]any{"i": n}),
					},
					StopReason: "tool_use",
					Timestamp:  time.Now().Unix(),
				}
				ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
				time.Sleep(2 * time.Millisecond)
				ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
			}()
			return ch, nil
		}
	}

	handlers := []func(req core.Request) (<-chan core.AssistantEvent, error){
		slowToolCall(0), slowToolCall(1), slowToolCall(2), slowToolCall(3),
		simpleTextResponse("done"),
	}
	ag := newTestAgent(NewMockProvider(handlers...), noopTool)

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 3; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = ag.Messages()
					_ = ag.CompactionEpoch()
				}
			}
		}()
	}

	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	close(stop)
	readers.Wait()
}

// --- Helper types ---

type testExtension struct {
	initFunc func(api extension.API) error
}

func (e *testExtension) Init(api extension.API) error {
	return e.initFunc(api)
}

// stopReasonResponse returns a handler that streams a text message with a given
// stop reason (and optional ErrorMessage/Usage), for exercising pause_turn and
// refusal handling in the loop.
func stopReasonResponse(text, stopReason, errorMessage string, usage *core.Usage) func(req core.Request) (<-chan core.AssistantEvent, error) {
	return func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role:         "assistant",
				Content:      []core.Content{core.TextContent(text)},
				StopReason:   stopReason,
				ErrorMessage: errorMessage,
				Usage:        usage,
				Timestamp:    time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, ContentIndex: 0, Delta: text}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}
}

func stopReasonToolCallResponse(id, name, stopReason string) func(req core.Request) (<-chan core.AssistantEvent, error) {
	return func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 2)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role:       "assistant",
				Content:    []core.Content{core.ToolCallContent(id, name, nil)},
				StopReason: stopReason,
				Timestamp:  time.Now().Unix(),
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}
}

// TestLoop_PauseTurnResubmits verifies a pause_turn triggers an automatic
// resubmit: the paused message is kept and the conversation continues to a
// clean completion, with both assistant messages in state.
func TestLoop_PauseTurnResubmits(t *testing.T) {
	provider := NewMockProvider(
		stopReasonResponse("Thinking...", "pause_turn", "", nil),
		stopReasonResponse("Here is the answer.", "end_turn", "", nil),
	)
	ag := newTestAgent(provider)

	msgs, err := ag.Run(context.Background(), "Do a long task")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// user + paused assistant + final assistant.
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != "assistant" || msgs[1].Content[0].Text != "Thinking..." {
		t.Errorf("paused message not preserved: %+v", msgs[1])
	}
	if msgs[2].Role != "assistant" || msgs[2].Content[0].Text != "Here is the answer." {
		t.Errorf("continuation missing: %+v", msgs[2])
	}
	if provider.calls != 2 {
		t.Errorf("provider should be called twice, got %d", provider.calls)
	}
}

// TestLoop_PauseTurnCap verifies an endless pause_turn loop is capped and
// surfaces an error rather than spinning forever.
func TestLoop_PauseTurnCap(t *testing.T) {
	handlers := make([]func(core.Request) (<-chan core.AssistantEvent, error), 20)
	for i := range handlers {
		handlers[i] = stopReasonResponse("still pausing", "pause_turn", "", nil)
	}
	provider := NewMockProvider(handlers...)
	ag := newTestAgent(provider)
	ag.config.MaxTurns = 100 // ensure the pause cap fires first
	events := collectEvents(ag)

	_, err := ag.Run(context.Background(), "go")
	if err == nil {
		t.Fatal("expected pause_turn cap error")
	}
	if !strings.Contains(err.Error(), "pause_turn") {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.calls != maxPauseTurnResubmits {
		t.Errorf("expected exactly %d provider calls, got %d", maxPauseTurnResubmits, provider.calls)
	}
	if !waitForEvent(events, core.AgentEventError, 500*time.Millisecond) {
		t.Error("missing agent_error event")
	}
}

// TestLoop_PauseTurnResetsCounter verifies the pause streak resets after a
// normal (non-pause) turn, so an occasional pause never trips the cap. Without
// the reset, the 2 early pauses + 4 later pauses would total 6 >= the cap and
// error; with the reset the tool_use turn clears the streak so the run finishes.
func TestLoop_PauseTurnResetsCounter(t *testing.T) {
	resetTool := core.Tool{
		Name:       "noop",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			return core.TextResult("ok"), nil
		},
	}
	provider := NewMockProvider(
		stopReasonResponse("p1", "pause_turn", "", nil),
		stopReasonResponse("p2", "pause_turn", "", nil),
		toolCallResponse("call_1", "noop", map[string]any{}), // tool_use → resets streak
		stopReasonResponse("p3", "pause_turn", "", nil),
		stopReasonResponse("p4", "pause_turn", "", nil),
		stopReasonResponse("p5", "pause_turn", "", nil),
		stopReasonResponse("p6", "pause_turn", "", nil),
		stopReasonResponse("done", "end_turn", "", nil),
	)
	ag := newTestAgent(provider, resetTool)
	ag.config.MaxTurns = 100

	_, err := ag.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("streak should have reset after tool_use, got: %v", err)
	}
	if provider.calls != 8 {
		t.Errorf("expected 8 provider calls, got %d", provider.calls)
	}
}

// TestLoop_PauseTurnAccumulatesBudget verifies the paused response's cost counts
// toward the budget so a pause loop can't bypass the limit.
func TestLoop_PauseTurnAccumulatesBudget(t *testing.T) {
	bigUsage := &core.Usage{Input: 1_000_000, Output: 1_000_000}
	provider := NewMockProvider(
		stopReasonResponse("pausing", "pause_turn", "", bigUsage),
		stopReasonResponse("should not reach", "end_turn", "", nil),
	)
	ag := newTestAgent(provider)
	ag.config.MaxBudget = 0.0001
	ag.config.Model = core.Model{ID: "test-model", Provider: "mock",
		Pricing: &core.Pricing{Input: 100, Output: 100}}

	_, err := ag.Run(context.Background(), "go")
	if err == nil {
		t.Fatal("expected budget exceeded error")
	}
	var budgetErr *BudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected BudgetExceededError, got %v", err)
	}
	// Only the first (paused) call should have run before the budget tripped.
	if provider.calls != 1 {
		t.Errorf("expected 1 provider call before budget trip, got %d", provider.calls)
	}
}

// TestLoop_RefusalSurfacesError verifies a refusal ends the run with a visible
// error carrying the explanation, while the partial refusal text stays in state.
func TestLoop_RefusalSurfacesError(t *testing.T) {
	provider := NewMockProvider(
		stopReasonResponse("I can't help with that.", "refusal", "This violates the policy.", nil),
	)
	ag := newTestAgent(provider)
	events := collectEvents(ag)

	msgs, err := ag.Run(context.Background(), "do something bad")
	if err == nil {
		t.Fatal("expected refusal error")
	}
	if !strings.Contains(err.Error(), "This violates the policy.") {
		t.Fatalf("error should carry explanation, got: %v", err)
	}
	// The refusal text must remain visible in history.
	if len(msgs) < 2 || msgs[1].Content[0].Text != "I can't help with that." {
		t.Errorf("refusal text should be preserved: %+v", msgs)
	}
	if !waitForEvent(events, core.AgentEventError, 500*time.Millisecond) {
		t.Error("missing agent_error event")
	}
	if !waitForEvent(events, core.AgentEventMessageEnd, 500*time.Millisecond) {
		t.Error("message_end should fire before the error")
	}
}

// TestLoop_RefusalNoExplanationFallback verifies a refusal with no explanation
// still produces a meaningful error.
func TestLoop_RefusalNoExplanationFallback(t *testing.T) {
	provider := NewMockProvider(
		stopReasonResponse("No.", "refusal", "", nil),
	)
	ag := newTestAgent(provider)
	_, err := ag.Run(context.Background(), "go")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("expected fallback 'refused' text, got: %v", err)
	}
}

// TestLoop_SensitiveSurfacesError verifies a "sensitive" stop reason surfaces a
// safety-filter error.
func TestLoop_SensitiveSurfacesError(t *testing.T) {
	provider := NewMockProvider(
		stopReasonResponse("partial", "sensitive", "", nil),
	)
	ag := newTestAgent(provider)
	_, err := ag.Run(context.Background(), "go")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "safety filters") {
		t.Fatalf("expected safety-filter error, got: %v", err)
	}
}

// TestLoop_UnknownStopReasonCompletes verifies an unrecognized stop reason with
// no tool calls ends the run cleanly (no crash, no error).
func TestLoop_UnknownStopReasonCompletes(t *testing.T) {
	provider := NewMockProvider(
		stopReasonResponse("all done", "weird_future_value", "", nil),
	)
	ag := newTestAgent(provider)
	msgs, err := ag.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("unknown stop reason should complete cleanly, got: %v", err)
	}
	if len(msgs) != 2 || msgs[1].Content[0].Text != "all done" {
		t.Errorf("unexpected messages: %+v", msgs)
	}
}

func TestLoop_EmptyMaxTokensResubmitsOnce(t *testing.T) {
	provider := NewMockProvider(
		stopReasonResponse("", "max_tokens", "", nil),
		stopReasonResponse("recovered", "end_turn", "", nil),
	)
	ag := newTestAgent(provider)

	msgs, err := ag.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("empty max_tokens should be retried, got: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("expected 2 provider calls, got %d", provider.calls)
	}
	if got := msgs[len(msgs)-1].Content[0].Text; got != "recovered" {
		t.Fatalf("final message = %q, want recovered", got)
	}
}

func TestLoop_MaxTokensWithTextErrors(t *testing.T) {
	provider := NewMockProvider(stopReasonResponse("partial", "max_tokens", "", nil))
	ag := newTestAgent(provider)

	_, err := ag.Run(context.Background(), "go")
	if err == nil || !strings.Contains(err.Error(), "output truncated") {
		t.Fatalf("expected truncation error, got: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("expected 1 provider call, got %d", provider.calls)
	}
}

func TestLoop_MaxTokensWithToolCallKeepsTranscriptValid(t *testing.T) {
	provider := NewMockProvider(stopReasonToolCallResponse("call_1", "noop", "max_tokens"))
	ag := newTestAgent(provider)

	msgs, err := ag.Run(context.Background(), "go")
	if err == nil || !strings.Contains(err.Error(), "output truncated") {
		t.Fatalf("expected truncation error, got: %v", err)
	}
	if len(msgs) != 3 || msgs[2].Role != "tool_result" || !msgs[2].IsError {
		t.Fatalf("truncated tool call must receive an error result, got: %+v", msgs)
	}
}

// emptyResponseProvider returns a handler that emits a typed EmptyResponseError
// (optionally carrying usage) via a stream error (no Done), like the openai
// provider does for an empty turn.
func emptyResponseProvider(usage *core.Usage) func(req core.Request) (<-chan core.AssistantEvent, error) {
	return func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 4)
		go func() {
			defer close(ch)
			msg := core.Message{Role: "assistant", Timestamp: time.Now().Unix()}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: &core.EmptyResponseError{Provider: "openai", Usage: usage}}
		}()
		return ch, nil
	}
}

// TestLoop_ContinueResubmits verifies StopReason "continue" (OpenAI
// end_turn:false) resubmits the conversation until a normal turn ends the run.
func TestLoop_ContinueResubmits(t *testing.T) {
	provider := NewMockProvider(
		stopReasonResponse("", "continue", "", nil),
		stopReasonResponse("Here is the final answer.", "end_turn", "", nil),
	)
	ag := newTestAgent(provider)
	events := collectEvents(ag)

	msgs, err := ag.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.calls != 2 {
		t.Errorf("expected 2 provider calls, got %d", provider.calls)
	}
	if msgs[len(msgs)-1].Content[0].Text != "Here is the final answer." {
		t.Errorf("final message missing: %+v", msgs[len(msgs)-1])
	}
	if waitForEvent(events, core.AgentEventError, 200*time.Millisecond) {
		t.Error("continue must not raise an error event")
	}
}

// TestLoop_ContinueCapNoProgress verifies an endless no-progress "continue" loop
// is capped and surfaces an error.
func TestLoop_ContinueCapNoProgress(t *testing.T) {
	handlers := make([]func(core.Request) (<-chan core.AssistantEvent, error), 20)
	for i := range handlers {
		handlers[i] = stopReasonResponse("", "continue", "", nil)
	}
	provider := NewMockProvider(handlers...)
	ag := newTestAgent(provider)
	ag.config.MaxTurns = 100
	_, err := ag.Run(context.Background(), "go")
	if err == nil {
		t.Fatal("expected continuation cap error")
	}
	if !strings.Contains(err.Error(), "continuation") {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.calls != maxEmptyContinuations {
		t.Errorf("expected %d calls, got %d", maxEmptyContinuations, provider.calls)
	}
}

// TestLoop_ContinueProgressResetsStreak verifies a "continue" that carries text
// resets the no-progress streak, so continuations with progress never trip the
// cap. Without the reset, 6 continuations would exceed maxEmptyContinuations.
func TestLoop_ContinueProgressResetsStreak(t *testing.T) {
	var handlers []func(core.Request) (<-chan core.AssistantEvent, error)
	for i := 0; i < 6; i++ {
		handlers = append(handlers, stopReasonResponse("progress chunk", "continue", "", nil))
	}
	handlers = append(handlers, stopReasonResponse("done", "end_turn", "", nil))
	provider := NewMockProvider(handlers...)
	ag := newTestAgent(provider)
	ag.config.MaxTurns = 100
	_, err := ag.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("progress should reset the streak, got: %v", err)
	}
	if provider.calls != 7 {
		t.Errorf("expected 7 provider calls, got %d", provider.calls)
	}
}

// TestLoop_EmptyResponseRetriesOnce verifies a typed empty response is
// re-sampled once (same request, nothing added to history) and then succeeds.
func TestLoop_EmptyResponseRetriesOnce(t *testing.T) {
	provider := NewMockProvider(
		emptyResponseProvider(nil),
		simpleTextResponse("recovered"),
	)
	ag := newTestAgent(provider)
	events := collectEvents(ag)

	msgs, err := ag.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("empty response should be retried, got: %v", err)
	}
	if provider.calls != 2 {
		t.Errorf("expected 2 provider calls (1 empty + 1 retry), got %d", provider.calls)
	}
	// user + the recovered assistant message only; the empty turn adds nothing.
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[1].Content[0].Text != "recovered" {
		t.Errorf("unexpected final message: %+v", msgs[1])
	}
	if waitForEvent(events, core.AgentEventError, 200*time.Millisecond) {
		t.Error("a retried empty response must not raise an error event")
	}
}

// TestLoop_EmptyResponseTwiceErrors verifies two consecutive empty responses
// (retry exhausted) surface a visible error.
func TestLoop_EmptyResponseTwiceErrors(t *testing.T) {
	provider := NewMockProvider(
		emptyResponseProvider(nil),
		emptyResponseProvider(nil),
	)
	ag := newTestAgent(provider)
	_, err := ag.Run(context.Background(), "go")
	if err == nil {
		t.Fatal("expected empty-response error after retry exhausted")
	}
	if !strings.Contains(err.Error(), "empty response") {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.calls != 2 {
		t.Errorf("expected exactly 2 provider calls, got %d", provider.calls)
	}
}

// TestLoop_EmptyRetryResetsAfterSuccess verifies the retry budget is per stall
// point: empty → success → empty → success uses one retry per empty.
func TestLoop_EmptyRetryResetsAfterSuccess(t *testing.T) {
	provider := NewMockProvider(
		emptyResponseProvider(nil),
		toolCallResponse("call_1", "noop", map[string]any{}),
		emptyResponseProvider(nil),
		simpleTextResponse("done"),
	)
	noopTool := core.Tool{
		Name:       "noop",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			return core.TextResult("ok"), nil
		},
	}
	ag := newTestAgent(provider, noopTool)
	_, err := ag.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("each empty should get its own retry, got: %v", err)
	}
	if provider.calls != 4 {
		t.Errorf("expected 4 provider calls, got %d", provider.calls)
	}
}

// TestLoop_EmptyResponseAccountsCostBeforeRetry verifies the token usage of an
// empty response is charged to the budget, so a stall can't bypass MaxBudget by
// looping through empty turns. The first empty response's usage alone exceeds
// the budget → the run stops without the retry provider call.
func TestLoop_EmptyResponseAccountsCostBeforeRetry(t *testing.T) {
	bigUsage := &core.Usage{Input: 1_000_000, Output: 0}
	provider := NewMockProvider(
		emptyResponseProvider(bigUsage),
		simpleTextResponse("should not reach"),
	)
	ag := newTestAgent(provider)
	ag.config.MaxBudget = 0.0001
	ag.config.Model = core.Model{ID: "test-model", Provider: "mock",
		Pricing: &core.Pricing{Input: 100, Output: 100}}

	_, err := ag.Run(context.Background(), "go")
	if err == nil {
		t.Fatal("expected an error (budget or empty)")
	}
	// The empty usage (1M input @ $100/M = $100) blows the $0.0001 budget, so
	// the run must stop at the budget pre-check before the retry provider call.
	var budgetErr *BudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected BudgetExceededError, got %v", err)
	}
	if provider.calls != 1 {
		t.Errorf("expected exactly 1 provider call (no retry after budget blown), got %d", provider.calls)
	}
}
