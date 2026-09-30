package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// A provider can hand over a Done and close its channel after the run was
// cancelled. Both are ready when consumeStream reads them, and select picks
// among ready cases at random, so the check is repeated: each iteration fails
// one time in four when a closed channel can win over the cancellation.
func TestConsumeStreamCancelledBeforeCloseReportsCancellation(t *testing.T) {
	msg := core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent("late-call", "block", nil)}}
	emitter := NewEmitter(nil)
	for i := range 200 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ch := make(chan core.AssistantEvent, 1)
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		close(ch)
		if _, err := consumeStream(ctx, ch, emitter); !errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d: consumeStream err = %v, want context.Canceled", i, err)
		}
	}
}

// Stop can land after the stream was accepted, while a call is still in
// pre-flight. No tool of that stopped run may start, and the call is still
// closed in history so the conversation stays valid.
func TestAbortDuringPreflightDoesNotStartTool(t *testing.T) {
	provider := NewMockProvider(func(core.Request) (<-chan core.AssistantEvent, error) {
		msg := core.Message{Role: "assistant", Content: []core.Content{core.ToolCallContent("toolu_preflight", "block", nil)}}
		ch := make(chan core.AssistantEvent, 1)
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
		close(ch)
		return ch, nil
	})
	var executed atomic.Bool
	block := core.Tool{
		Name:       "block",
		Parameters: []byte(`{"type":"object"}`),
		Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			executed.Store(true)
			return core.TextResult("side effect"), nil
		},
	}
	ag := newTestAgent(provider, block)
	if err := ag.SetPermissionCheck(func(context.Context, string, map[string]any) *core.ToolCallDecision {
		ag.Abort()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := ag.Run(context.Background(), "run the tool")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("aborted run did not finish")
	}
	if executed.Load() {
		t.Fatal("tool started after Stop")
	}
	msgs := ag.Messages()
	if len(msgs) != 3 || msgs[1].Role != "assistant" || msgs[2].Role != "tool_result" {
		t.Fatalf("stopped tool call was not closed: %+v", msgs)
	}
	if msgs[2].ToolCallID != "toolu_preflight" || !msgs[2].IsError {
		t.Fatalf("synthetic result = %+v, want error for toolu_preflight", msgs[2])
	}
}
