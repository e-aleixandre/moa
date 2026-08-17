package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

// TestConsumeStream_CancelledTurnWithLateDoneIsNotSuccess pins M18: when the
// turn is cancelled but the provider still delivers a complete final message
// within the drain window, consumeStream must return that message WITH a
// cancellation error — never a nil error. A nil error would make the run loop
// treat the cancelled turn as a clean success and execute its tool calls.
func TestConsumeStream_CancelledTurnWithLateDoneIsNotSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan core.AssistantEvent, 4)

	// Cancel first, then queue a complete Done message so the drain path sees it.
	cancel()
	final := &core.Message{Role: "assistant", Content: []core.Content{core.TextContent("done")}}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: final}

	msg, err := consumeStream(ctx, ch, NewEmitter(nil))

	if err == nil {
		t.Fatal("cancelled turn returned nil error — caller would execute its tool calls")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if msg != final {
		t.Fatalf("msg = %v, want the complete final message preserved for history", msg)
	}
}

type cancelledToolCallProvider struct {
	started chan struct{}
}

func (p *cancelledToolCallProvider) Stream(ctx context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
	ch := make(chan core.AssistantEvent, 1)
	close(p.started)
	go func() {
		defer close(ch)
		<-ctx.Done()
		msg := &core.Message{
			Role: "assistant",
			Content: []core.Content{{
				Type:       "tool_call",
				ToolCallID: "toolu_cancelled",
				ToolName:   "block",
			}},
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: msg}
	}()
	return ch, nil
}

func TestAbortWithLateToolCallKeepsHistoryBalanced(t *testing.T) {
	provider := &cancelledToolCallProvider{started: make(chan struct{})}
	var executed atomic.Bool
	block := core.Tool{
		Name:       "block",
		Parameters: []byte(`{"type":"object"}`),
		Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			executed.Store(true)
			return core.TextResult("unexpected"), nil
		},
	}
	ag := newTestAgent(provider, block)
	done := make(chan error, 1)
	go func() {
		_, err := ag.Run(context.Background(), "run the tool")
		done <- err
	}()

	<-provider.started
	ag.Abort()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("aborted run did not finish")
	}
	if executed.Load() {
		t.Fatal("tool was executed after cancellation")
	}

	msgs := ag.Messages()
	if len(msgs) != 3 || msgs[1].Role != "assistant" || msgs[2].Role != "tool_result" {
		t.Fatalf("cancelled tool call was not closed: %+v", msgs)
	}
	if msgs[2].ToolCallID != "toolu_cancelled" || !msgs[2].IsError {
		t.Fatalf("synthetic result = %+v, want cancelled error for toolu_cancelled", msgs[2])
	}

	tree := session.NewTree()
	for _, msg := range msgs {
		tree.Append(session.Entry{Type: session.EntryMessage, Message: msg})
	}
	tree.Append(session.Entry{
		Type: session.EntryMessage,
		Message: core.WrapMessage(core.Message{
			Role:    "assistant",
			Content: []core.Content{core.TextContent("balance probe")},
		}),
	})
	if err := tree.ValidBranchTarget(tree.LeafID()); err != nil {
		t.Fatalf("persisted context has a dangling tool call: %v", err)
	}
}

// A tool's Custom annotations reach the recorded tool_result: that map is how
// the subagent tool records which job a call spawned, and the UI reads it back
// from the transcript long after the job is gone.
func TestToolResultMessageCarriesResultCustom(t *testing.T) {
	call := core.Content{Type: "tool_call", ToolCallID: "toolu_1", ToolName: "subagent"}
	result := core.Result{Content: []core.Content{core.TextContent("done")}, Custom: map[string]any{"subagent_job_id": "sa-1"}}

	msg := toolResultMessage(call, result, false, false)
	if got := msg.Custom["subagent_job_id"]; got != "sa-1" {
		t.Fatalf("subagent_job_id = %v, want sa-1", got)
	}

	rejected := toolResultMessage(call, result, false, true)
	if rejected.Custom["subagent_job_id"] != "sa-1" || rejected.Custom["rejected"] != true {
		t.Fatalf("annotations and rejection must coexist, got %+v", rejected.Custom)
	}
}
