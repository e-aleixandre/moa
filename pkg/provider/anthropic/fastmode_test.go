package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func fastBody(t *testing.T, modelID string, fast bool) map[string]any {
	t.Helper()
	model, ok := core.ResolveModel(modelID)
	if !ok {
		t.Fatalf("unknown model %q", modelID)
	}
	req := core.Request{
		Model:    model,
		Messages: []core.Message{{Role: "user", Content: []core.Content{core.TextContent("hi")}}},
		Options:  core.StreamOptions{Fast: fast},
	}
	raw, err := buildRequestBody(req, true)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return body
}

func TestFastModeSendsSpeedOnOpus(t *testing.T) {
	body := fastBody(t, "claude-opus-5", true)
	if body["speed"] != "fast" {
		t.Errorf(`opus with fast mode on sent speed=%v, want "fast" — without it the request is served at standard speed and the user pays nothing extra but waits`, body["speed"])
	}
}

func TestFastModeOmittedWhenOff(t *testing.T) {
	body := fastBody(t, "claude-opus-5", false)
	if _, present := body["speed"]; present {
		t.Errorf("speed was sent with fast mode off (%v): every request would be billed at the premium rate", body["speed"])
	}
}

func TestFastModeOmittedOnModelsThatRejectIt(t *testing.T) {
	// Sonnet answers 400 "does not support the `speed` parameter": sending it
	// would break the turn outright rather than costing more.
	body := fastBody(t, "claude-sonnet-5", true)
	if _, present := body["speed"]; present {
		t.Errorf("speed was sent to a non-Opus model (%v), which the API rejects with a 400", body["speed"])
	}
}

func TestFastModeUnavailableIsNotRetried(t *testing.T) {
	// The API rejects a fast-mode request from an account with no usage
	// credits using this exact wording. It is a verdict, not a throttle:
	// retrying it five times with backoff only delays the fallback.
	creditsGone := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for fast mode."}}`)
	if !isFastModeUnavailable(creditsGone) {
		t.Error("the credits-exhausted 429 was treated as a transient rate limit, so the turn would stall ~30s retrying a request that can never succeed")
	}

	ordinary := []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit."}}`)
	if isFastModeUnavailable(ordinary) {
		t.Error("an ordinary rate limit was mistaken for a fast-mode rejection, so a retryable request would be given up on")
	}
}

func TestFastModeBetaTravelsWithSpeed(t *testing.T) {
	// Without the beta header the API rejects the field itself with
	// "speed: Extra inputs are not permitted", so the two must ship together.
	if !strings.HasPrefix(fastModeBeta, "fast-mode-") {
		t.Errorf("fastModeBeta = %q, which is not the beta the API accepts", fastModeBeta)
	}
}

// TestFastModeFallsBackToStandardSpeed pins the behaviour the owner asked for:
// try fast, and if the account has no usage credits for it, run the same turn
// at standard speed rather than failing. On a subscription the ordinary quota
// is untouched by the fast-mode refusal, so the work still gets done.
func TestFastModeFallsBackToStandardSpeed(t *testing.T) {
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"m1","model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	var speeds []any
	callbackCalls := 0
	callbackBeforeSlow := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		speeds = append(speeds, body["speed"])
		if body["speed"] != "fast" {
			callbackBeforeSlow = callbackCalls == 1
		}

		if body["speed"] == "fast" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for fast mode."}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	model, _ := core.ResolveModel("claude-opus-5")
	a := NewWithBaseURL("sk-ant-api03-test", srv.URL)
	ch, err := a.Stream(context.Background(), core.Request{
		Model:    model,
		Messages: []core.Message{core.NewUserMessage("hi")},
		Options: core.StreamOptions{Fast: true, OnFastUnavailable: func() {
			callbackCalls++
		}},
	})
	if err != nil {
		t.Fatalf("the turn failed instead of falling back to standard speed: %v", err)
	}
	for ev := range ch {
		if ev.Type == core.ProviderEventError && ev.Error != nil {
			t.Fatalf("the fallback stream errored: %v", ev.Error)
		}
	}

	if len(speeds) != 2 {
		t.Fatalf("sent %d requests %v, want 2: one fast attempt and one standard retry", len(speeds), speeds)
	}
	if speeds[0] != "fast" {
		t.Errorf("first attempt sent speed=%v, want the fast one to be tried first", speeds[0])
	}
	if speeds[1] != nil {
		t.Errorf("the retry still asked for speed=%v; it must drop fast mode or it is refused again", speeds[1])
	}
	if callbackCalls != 1 {
		t.Errorf("fast-unavailable callback calls = %d, want 1", callbackCalls)
	}
	if !callbackBeforeSlow {
		t.Error("fast-unavailable callback ran after the standard-speed retry")
	}
}
