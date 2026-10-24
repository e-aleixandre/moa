package anthropic

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestParseRateLimit_Resets(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-status", "allowed")
	h.Set("anthropic-ratelimit-unified-5h-utilization", "0.17")
	h.Set("anthropic-ratelimit-unified-5h-reset", "1790000000")
	h.Set("anthropic-ratelimit-unified-7d-utilization", "0.02")
	h.Set("anthropic-ratelimit-unified-7d-reset", "garbage")
	rl := parseRateLimit(h)
	if rl.FiveHourResetAt != 1790000000 {
		t.Errorf("FiveHourResetAt = %d", rl.FiveHourResetAt)
	}
	if rl.SevenDayResetAt != 0 {
		t.Errorf("malformed reset must read as unknown (0), got %d", rl.SevenDayResetAt)
	}
}

// The quota readings of the response that produced a message are persisted on
// that message, so every request leaves a durable, content-free record.
func TestStream_StampsQuotaOnFinalMessage(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m1","model":"claude-x","usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("anthropic-ratelimit-unified-status", "allowed")
		w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "0.25")
		w.Header().Set("anthropic-ratelimit-unified-5h-reset", "1790000000")
		w.Header().Set("anthropic-ratelimit-unified-7d-utilization", "0.5")
		w.Header().Set("anthropic-ratelimit-unified-7d-reset", "1790500000")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	a := NewWithBaseURL("sk-ant-api03-test", srv.URL)
	ch, err := a.Stream(context.Background(), core.Request{Model: core.Model{ID: "claude-x"}, Messages: []core.Message{core.NewUserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	var final *core.Message
	for ev := range ch {
		if ev.Type == core.ProviderEventDone {
			final = ev.Message
		}
	}
	if final == nil || final.RateLimit == nil {
		t.Fatalf("final message carries no quota: %+v", final)
	}
	rl := final.RateLimit
	if rl.FiveHourUtil != 0.25 || rl.FiveHourResetAt != 1790000000 || rl.SevenDayUtil != 0.5 || rl.SevenDayResetAt != 1790500000 {
		t.Errorf("rate limit = %+v", rl)
	}
}

// A response without quota headers (API key) leaves the field absent instead
// of recording a bogus 0%.
func TestStream_NoQuotaHeadersNoField(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m1","model":"claude-x","usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()
	a := NewWithBaseURL("sk-ant-api03-test", srv.URL)
	ch, err := a.Stream(context.Background(), core.Request{Model: core.Model{ID: "claude-x"}, Messages: []core.Message{core.NewUserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range ch {
		if ev.Type == core.ProviderEventDone && ev.Message.RateLimit != nil {
			t.Errorf("unexpected quota: %+v", ev.Message.RateLimit)
		}
	}
}

func TestStream_DebugLogsRequestIDWithoutContent(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m1","model":"claude-x","usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("request-id", "req_test123")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	run := func() string {
		var buf bytes.Buffer
		prev := slog.Default()
		defer slog.SetDefault(prev)
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
		a := NewWithBaseURL("sk-ant-api03-test", srv.URL)
		ch, err := a.Stream(context.Background(), core.Request{Model: core.Model{ID: "claude-x"}, Messages: []core.Message{core.NewUserMessage("TOPSECRETTEXT")}, Options: core.StreamOptions{PromptCacheKey: "moa:session:k"}})
		if err != nil {
			t.Fatal(err)
		}
		for range ch {
		}
		return buf.String()
	}

	t.Setenv("MOA_DEBUG_CACHE", "")
	if out := run(); out != "" {
		t.Errorf("logged without the flag: %s", out)
	}
	t.Setenv("MOA_DEBUG_CACHE", "1")
	out := run()
	if !strings.Contains(out, "request_id=req_test123") || !strings.Contains(out, "cache_key=moa:session:k") {
		t.Errorf("missing request-id line: %s", out)
	}
	if strings.Contains(out, "TOPSECRETTEXT") || strings.Contains(out, "sk-ant") {
		t.Errorf("log leaks content or secrets: %s", out)
	}
}
