package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/sseutil"
)

func anthropicMidStreamErrorSSE(errType string) string {
	return `event: message_start
data: {"type":"message_start","message":{"id":"msg_cut","role":"assistant","model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial "}}

event: error
data: {"type":"error","error":{"type":"` + errType + `","message":"synthetic"}}

`
}

// An Anthropic error event in the middle of a stream is classified by its
// type: overload and server errors are repaired like a transport cut, while a
// request error ends the turn without replaying it.
func TestStreamRepair_AnthropicMidStreamErrors(t *testing.T) {
	for _, tc := range []struct {
		errType  string
		requests int
		ok       bool
	}{
		{"overloaded_error", 2, true},
		{"api_error", 2, true},
		{"invalid_request_error", 1, false},
	} {
		t.Run(tc.errType, func(t *testing.T) {
			provider, captured := anthropicTailTestServer(t, "synthetic-not-a-credential", anthropicMidStreamErrorSSE(tc.errType))
			ag, err := New(AgentConfig{Provider: provider, Model: anthropicTailModel(t), MaxTurns: 3, StreamRepairBackoff: []time.Duration{0, 0}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, runErr := ag.Send(ctx, "synthetic task")
			if len(captured) != tc.requests {
				t.Fatalf("got %d HTTP requests, want %d; run error: %v", len(captured), tc.requests, runErr)
			}
			if !tc.ok {
				if runErr == nil || !strings.Contains(runErr.Error(), tc.errType) {
					t.Fatalf("run error = %v, want %s", runErr, tc.errType)
				}
				return
			}
			if runErr != nil {
				t.Fatalf("repaired turn must complete: %v", runErr)
			}
			history := ag.Messages()
			last := history[len(history)-1]
			if last.Role != "assistant" || joinText(last.Content) != "partial done" {
				t.Fatalf("last message = %+v, want the merged assistant \"partial done\"", last)
			}
		})
	}
}

// An idle timeout is a stalled connection, the same class as a cut one.
func TestStreamRepair_IdleTimeoutIsRepaired(t *testing.T) {
	provider := NewMockProvider(
		func(core.Request) (<-chan core.AssistantEvent, error) {
			ch := make(chan core.AssistantEvent, 2)
			ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "Hello"}
			ch <- core.AssistantEvent{Type: core.ProviderEventError, Error: fmt.Errorf("SSE parse: %w", sseutil.ErrIdleTimeout)}
			close(ch)
			return ch, nil
		},
		func(core.Request) (<-chan core.AssistantEvent, error) {
			ch := make(chan core.AssistantEvent, 1)
			msg := core.Message{Role: "assistant", Content: []core.Content{core.TextContent(" world")}, StopReason: "end_turn", Timestamp: time.Now().Unix()}
			ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
			close(ch)
			return ch, nil
		},
	)
	ag := newTestAgent(provider)
	ag.config.StreamRepairBackoff = []time.Duration{0, 0}
	if _, err := ag.Run(context.Background(), "start"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("Stream calls = %d, want 2", provider.calls)
	}
}

// A provider's terminal verdict wins over words in its message that would
// otherwise look like a transport cut.
func TestIsRetryableStreamError_ProviderVerdictWins(t *testing.T) {
	terminal := &core.StreamFailureError{Err: errors.New("anthropic invalid_request_error: unexpected EOF in tool input"), Transient: false}
	if isRetryableStreamError(fmt.Errorf("stream: %w", terminal)) {
		t.Error("terminal provider error was repaired")
	}
	transient := &core.StreamFailureError{Err: errors.New("anthropic overloaded_error: Overloaded"), Transient: true}
	if !isRetryableStreamError(fmt.Errorf("stream: %w", transient)) {
		t.Error("transient provider error was not repaired")
	}
}
