package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ealeixandre/go-agent/pkg/core"
	"github.com/ealeixandre/go-agent/pkg/extension"
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
		reg.Register(t)
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
	// Provider always returns a tool call → infinite loop if no guardrail
	infiniteTool := func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 5)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.ToolCallContent("tc-loop", "noop", nil),
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
	reg.Register(bashTool)
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
	reg.Register(echoTool)
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

// --- Helper types ---

type testExtension struct {
	initFunc func(api extension.API) error
}

func (e *testExtension) Init(api extension.API) error {
	return e.initFunc(api)
}
