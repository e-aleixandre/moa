package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
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
