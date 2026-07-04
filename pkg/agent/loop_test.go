package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
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
