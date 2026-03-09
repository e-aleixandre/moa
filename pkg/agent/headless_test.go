package agent

import (
	"context"
	"encoding/json"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// TestHeadless_ServerPattern simulates the usage pattern Specflow (and any
// HTTP server embedding Moa) will follow:
//
//  1. Create agent with custom tools and a subset of builtins
//  2. Subscribe to events before sending
//  3. Send a message, collect streamed events
//  4. Verify messages and event ordering
//  5. Send another message (multi-turn), verify state accumulates
func TestHeadless_ServerPattern(t *testing.T) {
	// Custom tool: echoes the input text.
	echoTool := core.Tool{
		Name:        "echo",
		Description: "Echoes the input text back",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Execute: func(ctx context.Context, params map[string]any, _ func(core.Result)) (core.Result, error) {
			return core.TextResult(params["text"].(string)), nil
		},
	}

	reg := core.NewRegistry()
	reg.Register(echoTool)

	// Turn 1: simple text response.
	// Turn 2: tool call to echo, then (after tool result) final text.
	prov := NewMockProvider(
		simpleTextResponse("Hello from turn 1"),
		toolCallResponse("tc1", "echo", map[string]any{"text": "echoed back"}),
		simpleTextResponse("Done after tool"),
	)

	ag, err := New(AgentConfig{
		Provider:            prov,
		Model:               core.Model{ID: "test", Provider: "mock"},
		SystemPrompt:        "You are a test agent.",
		Tools:               reg,
		MaxTurns:            10,
		MaxToolCallsPerTurn: 5,
		MaxRunDuration:      10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Subscribe before first Send.
	var events []core.AgentEvent
	var mu sync.Mutex
	unsub := ag.Subscribe(func(e core.AgentEvent) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})
	defer unsub()

	// --- Turn 1 ---
	msgs, err := ag.Send(context.Background(), "Hello")
	if err != nil {
		t.Fatal(err)
	}

	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages (user+assistant), got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("first message role = %q, want user", msgs[0].Role)
	}
	if msgs[len(msgs)-1].Role != "assistant" {
		t.Errorf("last message role = %q, want assistant", msgs[len(msgs)-1].Role)
	}

	// Poll for agent_end event (async delivery, no time.Sleep).
	waitForEvent := func(eventType string) bool {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			for _, e := range events {
				if e.Type == eventType {
					mu.Unlock()
					return true
				}
			}
			mu.Unlock()
			runtime.Gosched()
		}
		return false
	}

	if !waitForEvent(core.AgentEventEnd) {
		t.Fatal("timed out waiting for agent_end event")
	}

	// Assert partial order: agent_start before agent_end.
	mu.Lock()
	eventTypes := make([]string, len(events))
	for i, e := range events {
		eventTypes[i] = e.Type
	}
	mu.Unlock()

	startIdx, endIdx := -1, -1
	for i, et := range eventTypes {
		if et == core.AgentEventStart && startIdx == -1 {
			startIdx = i
		}
		if et == core.AgentEventEnd {
			endIdx = i
		}
	}
	if startIdx == -1 || endIdx == -1 || startIdx >= endIdx {
		t.Errorf("expected agent_start before agent_end, got types: %v", eventTypes)
	}

	turn1Count := len(msgs)

	// --- Turn 2: multi-turn with tool call ---
	// Clear events for turn 2 assertions.
	mu.Lock()
	events = nil
	mu.Unlock()

	msgs2, err := ag.Send(context.Background(), "Call echo please")
	if err != nil {
		t.Fatal(err)
	}

	// State should accumulate: more messages than turn 1.
	if len(msgs2) <= turn1Count {
		t.Errorf("multi-turn should accumulate: turn1=%d, turn2=%d", turn1Count, len(msgs2))
	}

	// Should contain a tool_result message (echo tool was called).
	hasToolResult := false
	for _, m := range msgs2 {
		if m.Role == "tool_result" {
			hasToolResult = true
			break
		}
	}
	if !hasToolResult {
		t.Error("expected a tool_result message from echo tool call")
	}

	// Wait for turn 2 events.
	if !waitForEvent(core.AgentEventEnd) {
		t.Fatal("timed out waiting for agent_end event on turn 2")
	}

	// Verify tool execution events were emitted.
	mu.Lock()
	hasToolExecStart := false
	hasToolExecEnd := false
	for _, e := range events {
		if e.Type == core.AgentEventToolExecStart && e.ToolName == "echo" {
			hasToolExecStart = true
		}
		if e.Type == core.AgentEventToolExecEnd && e.ToolName == "echo" {
			hasToolExecEnd = true
		}
	}
	mu.Unlock()
	if !hasToolExecStart {
		t.Error("expected tool_execution_start event for echo")
	}
	if !hasToolExecEnd {
		t.Error("expected tool_execution_end event for echo")
	}

	// Messages() should return the same state as Send's return value.
	msgsCopy := ag.Messages()
	if len(msgsCopy) != len(msgs2) {
		t.Errorf("Messages() returned %d messages, Send returned %d", len(msgsCopy), len(msgs2))
	}
}
