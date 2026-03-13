package agent

import (
	"context"
	"encoding/json"
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
	reg.Register(sleepTool)
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
	reg.Register(sleepTool)
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
	reg.Register(blockTool)
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
	got = append(got, core.WrapMessage(core.NewUserMessage("extra")))
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
		ag.Run(ctx, "block forever")
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
	ag.LoadMessages([]core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("first")),
		{Message: core.Message{
			Role:    "assistant",
			Content: []core.Content{{Type: "text", Text: "first response"}},
		}},
	})

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

func TestReconfigure_StripsThinking(t *testing.T) {
	prov := NewMockProvider(simpleTextResponse("ok"))
	ag, _ := New(AgentConfig{
		Provider: prov,
		Model:    core.Model{ID: "model-1", Provider: "anthropic"},
	})

	// Manually inject a message with thinking blocks.
	ag.LoadMessages([]core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("hello")),
		core.WrapMessage(core.Message{
			Role: "assistant",
			Content: []core.Content{
				core.ThinkingContent("secret reasoning"),
				core.TextContent("visible response"),
			},
		}),
	})

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

	ag.LoadMessages([]core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("hello")),
		core.WrapMessage(core.Message{
			Role: "assistant",
			Content: []core.Content{
				core.ThinkingContent("reasoning"),
				core.TextContent("response"),
			},
		}),
	})

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

	go ag.Run(ctx, "hello")
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

// --- Helper types ---

type testExtension struct {
	initFunc func(api extension.API) error
}

func (e *testExtension) Init(api extension.API) error {
	return e.initFunc(api)
}
