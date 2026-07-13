package openai

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestParseRateLimit_Headers(t *testing.T) {
	tests := []struct {
		name        string
		headers     map[string]string
		wantNil     bool
		wantFiveH   float64
		wantSevenD  float64
		wantOverage float64
	}{
		{
			name: "both windows",
			headers: map[string]string{
				"x-codex-primary-used-percent":   "40",
				"x-codex-secondary-used-percent": "51",
			},
			wantFiveH: 0.40, wantSevenD: 0.51, wantOverage: -1,
		},
		{
			name: "fractional percent",
			headers: map[string]string{
				"x-codex-primary-used-percent":   "12.5",
				"x-codex-secondary-used-percent": "0.5",
			},
			wantFiveH: 0.125, wantSevenD: 0.005, wantOverage: -1,
		},
		{
			name: "only primary present -> weekly unknown",
			headers: map[string]string{
				"x-codex-primary-used-percent": "40",
			},
			wantFiveH: 0.40, wantSevenD: -1, wantOverage: -1,
		},
		{
			name: "over 100 clamps to 1",
			headers: map[string]string{
				"x-codex-primary-used-percent":   "137",
				"x-codex-secondary-used-percent": "100",
			},
			wantFiveH: 1.0, wantSevenD: 1.0, wantOverage: -1,
		},
		{
			name:    "no codex headers -> nil",
			headers: map[string]string{"content-type": "text/event-stream"},
			wantNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			rl := parseRateLimit(h)
			if tc.wantNil {
				if rl != nil {
					t.Fatalf("expected nil, got %+v", rl)
				}
				return
			}
			if rl == nil {
				t.Fatal("expected non-nil rate limit")
			}
			if rl.FiveHourUtil != tc.wantFiveH {
				t.Errorf("FiveHourUtil = %v, want %v", rl.FiveHourUtil, tc.wantFiveH)
			}
			if rl.SevenDayUtil != tc.wantSevenD {
				t.Errorf("SevenDayUtil = %v, want %v", rl.SevenDayUtil, tc.wantSevenD)
			}
			if rl.OverageUtil != tc.wantOverage {
				t.Errorf("OverageUtil = %v, want %v", rl.OverageUtil, tc.wantOverage)
			}
			if rl.OnOverage() {
				t.Error("ChatGPT/Codex has no overage bucket; OnOverage must be false")
			}
		})
	}
}

// rateLimitTestServer streams a minimal valid Responses SSE, with the given
// response headers attached.
func rateLimitTestServer(t *testing.T, headers map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = fmt.Fprint(w,
			sseEvent(`{"type":"response.output_item.added","item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":""}],"status":"in_progress"}}`)+
				sseEvent(`{"type":"response.output_text.delta","delta":"hi"}`)+
				sseEvent(`{"type":"response.output_item.done","item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hi"}],"status":"completed"}}`)+
				sseEvent(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`),
		)
	}))
}

// firstRateLimit drains the stream and returns the first rate-limit event's
// payload, or nil if none was emitted.
func firstRateLimit(t *testing.T, ch <-chan core.AssistantEvent) *core.RateLimit {
	t.Helper()
	var rl *core.RateLimit
	sawText := false
	for event := range ch {
		switch event.Type {
		case core.ProviderEventRateLimit:
			if rl == nil {
				rl = event.RateLimit
				if sawText {
					t.Error("rate-limit event must arrive before text deltas")
				}
			}
		case core.ProviderEventTextDelta:
			sawText = true
		case core.ProviderEventError:
			t.Fatalf("unexpected error: %v", event.Error)
		}
	}
	return rl
}

func TestStream_EmitsRateLimit_OAuth(t *testing.T) {
	server := rateLimitTestServer(t, map[string]string{
		"x-codex-primary-used-percent":   "40",
		"x-codex-secondary-used-percent": "51",
	})
	defer server.Close()

	// OAuth (Codex) provider: accountID set, custom base URL to the test server.
	prov := &OpenAI{
		apiKey:    "oauth-token",
		baseURL:   server.URL,
		endpoint:  codexEndpoint,
		accountID: "acct_123",
		client:    server.Client(),
	}
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.6-terra"},
		Messages: []core.Message{core.NewUserMessage("Hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	rl := firstRateLimit(t, ch)
	if rl == nil {
		t.Fatal("expected a rate-limit event from the OAuth backend")
	}
	if rl.FiveHourUtil != 0.40 || rl.SevenDayUtil != 0.51 {
		t.Fatalf("unexpected utilization: %+v", rl)
	}
}

func TestStream_NoRateLimit_APIKey(t *testing.T) {
	// Even if the server sends codex headers, an API-key provider (no accountID)
	// must NOT emit a rate-limit event: api.openai.com has no plan/quota.
	server := rateLimitTestServer(t, map[string]string{
		"x-codex-primary-used-percent":   "40",
		"x-codex-secondary-used-percent": "51",
	})
	defer server.Close()

	prov := NewWithBaseURL("sk-test", server.URL) // accountID == ""
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("Hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rl := firstRateLimit(t, ch); rl != nil {
		t.Fatalf("API-key path must not emit a rate-limit event, got %+v", rl)
	}
}

func TestStream_NoRateLimit_OAuthNoHeaders(t *testing.T) {
	// OAuth backend but the response carries no x-codex-* headers: emit nothing
	// rather than a bogus 0%.
	server := rateLimitTestServer(t, nil)
	defer server.Close()

	prov := &OpenAI{
		apiKey:    "oauth-token",
		baseURL:   server.URL,
		endpoint:  codexEndpoint,
		accountID: "acct_123",
		client:    server.Client(),
	}
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.6-terra"},
		Messages: []core.Message{core.NewUserMessage("Hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rl := firstRateLimit(t, ch); rl != nil {
		t.Fatalf("expected no rate-limit event without codex headers, got %+v", rl)
	}
}
