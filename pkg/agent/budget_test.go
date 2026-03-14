package agent

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// testPricing returns pricing where 1000 input + 500 output tokens cost $0.02.
// Input: $10/M, Output: $20/M → 1000*10/1M + 500*20/1M = 0.01 + 0.01 = 0.02
func testPricing() *core.Pricing {
	return &core.Pricing{Input: 10, Output: 20}
}

func testUsage() *core.Usage {
	return &core.Usage{Input: 1000, Output: 500, TotalTokens: 1500}
}

// textResponseWithUsage returns a handler that streams a text response with usage.
func textResponseWithUsage(text string, usage *core.Usage) func(req core.Request) (<-chan core.AssistantEvent, error) {
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

// toolCallWithUsage returns a handler that emits a tool call with usage.
func toolCallWithUsage(toolID, toolName string, args map[string]any, usage *core.Usage) func(req core.Request) (<-chan core.AssistantEvent, error) {
	return func(req core.Request) (<-chan core.AssistantEvent, error) {
		ch := make(chan core.AssistantEvent, 10)
		go func() {
			defer close(ch)
			msg := core.Message{
				Role: "assistant",
				Content: []core.Content{
					core.TextContent("Using tool."),
					core.ToolCallContent(toolID, toolName, args),
				},
				StopReason: "tool_use",
				Timestamp:  time.Now().Unix(),
				Usage:      usage,
			}
			ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		}()
		return ch, nil
	}
}

func TestBudget_ExceededAfterFirstMessage(t *testing.T) {
	// Budget is $0.01, but the response costs $0.02 → should fail.
	prov := NewMockProvider(
		textResponseWithUsage("hello", testUsage()),
	)

	ag, err := New(AgentConfig{
		Provider:     prov,
		Model:        core.Model{ID: "test", Provider: "mock", Pricing: testPricing()},
		SystemPrompt: "test",
		MaxBudget:    0.01,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = ag.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected budget exceeded error")
	}

	// errors.Is
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got: %v", err)
	}

	// errors.As for typed payload
	var budgetErr *BudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("expected BudgetExceededError, got: %T", err)
	}
	if budgetErr.Limit != 0.01 {
		t.Errorf("limit = %f, want 0.01", budgetErr.Limit)
	}
	if budgetErr.Spent < 0.02-0.001 || budgetErr.Spent > 0.02+0.001 {
		t.Errorf("spent = %f, want ~0.02", budgetErr.Spent)
	}
}

func TestBudget_ExceededMidRun_NoOrphanedToolCalls(t *testing.T) {
	// First turn: tool call (costs $0.02, budget is $0.03)
	// Second turn: text response (costs $0.02, total $0.04 > $0.03)
	// The tool call turn should complete (tool_result present), then budget check fires.
	echoTool := core.Tool{
		Name:        "echo",
		Description: "echoes input",
		Effect:      core.EffectReadOnly,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			return core.TextResult("echoed"), nil
		},
	}

	prov := NewMockProvider(
		// Turn 1: tool call (budget check after tools: $0.02 < $0.03, OK)
		toolCallWithUsage("tc1", "echo", map[string]any{}, testUsage()),
		// Turn 2: text response (budget check: $0.04 > $0.03, FAIL)
		textResponseWithUsage("done", testUsage()),
	)

	reg := core.NewRegistry()
	_ = reg.Register(echoTool)

	ag, err := New(AgentConfig{
		Provider:     prov,
		Model:        core.Model{ID: "test", Provider: "mock", Pricing: testPricing()},
		SystemPrompt: "test",
		Tools:        reg,
		MaxBudget:    0.03,
	})
	if err != nil {
		t.Fatal(err)
	}

	msgs, err := ag.Run(context.Background(), "go")
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got: %v", err)
	}

	// Verify no orphaned tool calls: every tool_call has a matching tool_result message.
	pendingCalls := map[string]bool{}
	for _, m := range msgs {
		for _, c := range m.Content {
			if c.Type == "tool_call" {
				pendingCalls[c.ToolCallID] = true
			}
		}
		if m.Role == "tool_result" && m.ToolCallID != "" {
			delete(pendingCalls, m.ToolCallID)
		}
	}
	if len(pendingCalls) > 0 {
		t.Errorf("orphaned tool calls: %v", pendingCalls)
	}
}

func TestBudget_Unlimited(t *testing.T) {
	// MaxBudget=0 → no enforcement.
	prov := NewMockProvider(
		textResponseWithUsage("hello", testUsage()),
	)

	ag, err := New(AgentConfig{
		Provider:     prov,
		Model:        core.Model{ID: "test", Provider: "mock", Pricing: testPricing()},
		SystemPrompt: "test",
		MaxBudget:    0, // unlimited
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = ag.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBudget_NoPricing_RejectsAtCreation(t *testing.T) {
	prov := NewMockProvider()
	_, err := New(AgentConfig{
		Provider:     prov,
		Model:        core.Model{ID: "test", Provider: "mock"}, // no Pricing
		SystemPrompt: "test",
		MaxBudget:    5.0,
	})
	if err == nil {
		t.Fatal("expected error for MaxBudget without Pricing")
	}
	if !strings.Contains(err.Error(), "Pricing") {
		t.Fatalf("expected pricing error, got: %v", err)
	}
}

func TestBudget_InvalidValues_RejectAtCreation(t *testing.T) {
	prov := NewMockProvider()
	cases := []struct {
		name   string
		budget float64
	}{
		{"negative", -1.0},
		{"NaN", math.NaN()},
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(AgentConfig{
				Provider:     prov,
				Model:        core.Model{ID: "test", Provider: "mock", Pricing: testPricing()},
				SystemPrompt: "test",
				MaxBudget:    tc.budget,
			})
			if err == nil {
				t.Fatal("expected error for invalid MaxBudget")
			}
		})
	}
}

func TestBudget_ExceededOnToolCallTurn(t *testing.T) {
	// Budget barely over the first response cost.
	// Turn 1: tool call costs $0.02, budget is $0.015 → should fail after tool execution.
	echoTool := core.Tool{
		Name:        "echo",
		Description: "echoes input",
		Effect:      core.EffectReadOnly,
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			return core.TextResult("echoed"), nil
		},
	}

	prov := NewMockProvider(
		toolCallWithUsage("tc1", "echo", map[string]any{}, testUsage()),
	)

	reg := core.NewRegistry()
	_ = reg.Register(echoTool)

	ag, err := New(AgentConfig{
		Provider:     prov,
		Model:        core.Model{ID: "test", Provider: "mock", Pricing: testPricing()},
		SystemPrompt: "test",
		Tools:        reg,
		MaxBudget:    0.015,
	})
	if err != nil {
		t.Fatal(err)
	}

	msgs, err := ag.Run(context.Background(), "go")
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got: %v", err)
	}

	// Tool results must still be present (no orphaned calls).
	hasToolResult := false
	for _, m := range msgs {
		if m.Role == "tool_result" {
			hasToolResult = true
		}
	}
	if !hasToolResult {
		t.Error("expected tool_result messages before budget abort")
	}
}


