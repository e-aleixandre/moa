package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestParseRateLimit_RealHeaders(t *testing.T) {
	// Values captured from a real /v1/messages response.
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-status", "allowed")
	h.Set("anthropic-ratelimit-unified-5h-utilization", "0.17")
	h.Set("anthropic-ratelimit-unified-7d-utilization", "0.02")
	h.Set("anthropic-ratelimit-unified-overage-status", "allowed")
	h.Set("anthropic-ratelimit-unified-overage-utilization", "0.0")
	h.Set("anthropic-ratelimit-unified-representative-claim", "five_hour")

	rl := parseRateLimit(h)
	if rl == nil {
		t.Fatal("expected rate limit, got nil")
	}
	if rl.Status != "allowed" {
		t.Errorf("Status = %q", rl.Status)
	}
	if rl.RepresentativeClaim != "five_hour" {
		t.Errorf("RepresentativeClaim = %q", rl.RepresentativeClaim)
	}
	if rl.FiveHourUtil != 0.17 || rl.SevenDayUtil != 0.02 {
		t.Errorf("utils = %v / %v", rl.FiveHourUtil, rl.SevenDayUtil)
	}
	if rl.OnOverage() {
		t.Error("OnOverage() should be false when representative-claim is five_hour")
	}
}

func TestParseRateLimit_OnOverage(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-status", "allowed")
	h.Set("anthropic-ratelimit-unified-representative-claim", "overage")
	h.Set("anthropic-ratelimit-unified-overage-utilization", "0.42")

	rl := parseRateLimit(h)
	if rl == nil {
		t.Fatal("expected rate limit, got nil")
	}
	if !rl.OnOverage() {
		t.Error("OnOverage() should be true when representative-claim is overage")
	}
	if rl.OverageUtil != 0.42 {
		t.Errorf("OverageUtil = %v, want 0.42", rl.OverageUtil)
	}
}

// TestStream_EmitsRateLimit verifies the full provider path: rate-limit response
// headers are parsed and emitted as a dedicated event at stream start (before the
// message content), so the signal survives even if the stream later errors.
func TestStream_EmitsRateLimit(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m1","model":"claude-x","usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("anthropic-ratelimit-unified-status", "allowed")
		w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "0.17")
		w.Header().Set("anthropic-ratelimit-unified-representative-claim", "overage")
		w.Header().Set("anthropic-ratelimit-unified-overage-utilization", "0.5")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	a := NewWithBaseURL("sk-ant-api03-test", srv.URL)
	ch, err := a.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "claude-x"},
		Messages: []core.Message{core.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var rlEvent *core.AssistantEvent
	sawDone := false
	for ev := range ch {
		e := ev
		if e.Type == core.ProviderEventRateLimit {
			rlEvent = &e
		}
		if e.Type == core.ProviderEventDone {
			sawDone = true
		}
		if e.Type == core.ProviderEventError && e.Error != nil {
			t.Fatalf("stream error: %v", e.Error)
		}
	}
	if !sawDone {
		t.Fatal("no done event")
	}
	if rlEvent == nil || rlEvent.RateLimit == nil {
		t.Fatal("no ratelimit event emitted")
	}
	rl := rlEvent.RateLimit
	if !rl.OnOverage() {
		t.Error("expected OnOverage() true")
	}
	if rl.FiveHourUtil != 0.17 || rl.OverageUtil != 0.5 {
		t.Errorf("utils = %v / %v", rl.FiveHourUtil, rl.OverageUtil)
	}
}

func TestParseRateLimit_AbsentReturnsNil(t *testing.T) {
	// No unified headers (e.g. non-OAuth request or endpoint change) → nil.
	h := http.Header{}
	h.Set("content-type", "text/event-stream")
	h.Set("retry-after", "5")
	if rl := parseRateLimit(h); rl != nil {
		t.Errorf("expected nil, got %+v", rl)
	}
}

// TestStream_StopsAfterTerminalEvent is the provider-stream contract that a
// returned stream emits exactly one terminal event. Providers can receive
// malformed trailing frames after message_stop, but they must not turn one
// completed response into done followed by error (or another done).
func TestStream_StopsAfterTerminalEvent(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m1","model":"claude-x","usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n" +
		"event: error\n" +
		`data: {"type":"error","error":{"type":"api_error","message":"must be ignored"}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	a := NewWithBaseURL("sk-ant-api03-test", srv.URL)
	ch, err := a.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "claude-x"},
		Messages: []core.Message{core.NewUserMessage("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var terminals []core.AssistantEvent
	for event := range ch {
		if event.IsTerminal() {
			terminals = append(terminals, event)
		}
	}

	if len(terminals) != 1 {
		t.Fatalf("terminal events = %d, want exactly 1", len(terminals))
	}
	if terminals[0].Type != core.ProviderEventDone {
		t.Fatalf("terminal event = %q, want done", terminals[0].Type)
	}
}
