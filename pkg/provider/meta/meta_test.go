package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

type rewriteTransport struct{ target *url.URL }

func (t rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r.URL.Scheme, r.URL.Host = t.target.Scheme, t.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

func completedStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
}

func TestStream_RequestShape(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apiEndpoint || r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("request = %s accept=%q", r.URL.Path, r.Header.Get("Accept"))
		}
		if r.Header.Get("Authorization") != "Bearer key-1" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers = %#v", r.Header)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("body: %v", err)
		}
		completedStream(w)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	p := New("key-1")
	p.client.Transport = rewriteTransport{target}
	ch, err := p.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "muse-spark-1.3"},
		Messages: []core.Message{core.NewUserMessage("hi")},
		Options:  core.StreamOptions{ThinkingLevel: "xhigh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if body["model"] != "muse-spark-1.3" || body["stream"] != true || body["store"] != false {
		t.Fatalf("body = %#v", body)
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("reasoning = %#v", body["reasoning"])
	}
	if body["max_output_tokens"] == nil {
		t.Fatal("max_output_tokens not sent")
	}
	if body["parallel_tool_calls"] != true {
		t.Fatalf("parallel_tool_calls = %#v", body["parallel_tool_calls"])
	}
	if _, ok := body["service_tier"]; ok {
		t.Fatalf("service_tier sent: %#v", body["service_tier"])
	}
}

func TestStream_OffBecomesMinimal(t *testing.T) {
	var effort string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		effort = body.Reasoning.Effort
		completedStream(w)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	p := New("key-1")
	p.client.Transport = rewriteTransport{target}
	ch, err := p.Stream(context.Background(), core.Request{Model: core.Model{ID: "muse-spark-1.3"}, Options: core.StreamOptions{ThinkingLevel: "off"}})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if effort != "minimal" {
		t.Fatalf("effort = %q, want minimal", effort)
	}
}

func TestStream_RejectsUnknownThinkingLevel(t *testing.T) {
	_, err := New("key-1").Stream(context.Background(), core.Request{Model: core.Model{ID: "muse-spark-1.3"}, Options: core.StreamOptions{ThinkingLevel: "turbo"}})
	if err == nil || !strings.Contains(err.Error(), "invalid thinking level") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewOAuth_RemintsOnceOn401(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			if r.Header.Get("Authorization") != "Bearer stale" {
				t.Error("first key not used")
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fresh" {
			t.Error("re-minted key not used")
		}
		completedStream(w)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	p := NewOAuth("stale", func(rejected string) (string, error) {
		if rejected != "stale" {
			t.Errorf("rejected = %q", rejected)
		}
		return "fresh", nil
	})
	p.client.Transport = rewriteTransport{target}
	ch, err := p.Stream(context.Background(), core.Request{Model: core.Model{ID: "muse-spark-1.3"}, Messages: []core.Message{core.NewUserMessage("hi")}, Options: core.StreamOptions{ThinkingLevel: "low"}})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestNewOAuth_SecondUnauthorizedIsTerminal(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	p := NewOAuth("stale", func(string) (string, error) { return "fresh", nil })
	p.client.Transport = rewriteTransport{target}
	_, err := p.Stream(context.Background(), core.Request{Model: core.Model{ID: "muse-spark-1.3"}, Options: core.StreamOptions{ThinkingLevel: "low"}})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("err = %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestStream_APIKeyNeverReminted(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	p := New("key-1")
	p.client.Transport = rewriteTransport{target}
	_, err := p.Stream(context.Background(), core.Request{Model: core.Model{ID: "muse-spark-1.3"}, Options: core.StreamOptions{ThinkingLevel: "low"}})
	if err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("err = %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

// Meta answers 429 both for an exhausted account and for the transient output
// rate limit it applies to reserved max_output_tokens capacity. Only the first
// is a quota error a caller should surface as such.
func TestClassifyHTTP_RateLimitVsQuota(t *testing.T) {
	transient := []byte(`{"error":{"code":"rate_limit_exceeded","message":"Output token rate limit exceeded.","type":"rate_limit_error"}}`)
	if err := classifyHTTP(http.StatusTooManyRequests, transient, http.Header{}); err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v", err)
	}
	quota := []byte(`{"error":{"code":"insufficient_quota","message":"You have exceeded your quota."}}`)
	err := classifyHTTP(http.StatusTooManyRequests, quota, http.Header{"Retry-After": {"30"}})
	quotaErr, ok := err.(*core.QuotaExceededError)
	if !ok {
		t.Fatalf("err = %#v", err)
	}
	if quotaErr.Provider != "meta" || quotaErr.ResetsIn.Seconds() != 30 {
		t.Fatalf("quota = %#v", quotaErr)
	}
}

func TestStream_QuotaErrorIsNotRetried(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":{"code":"insufficient_quota","message":"You have exceeded your quota."}}`)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	p := New("key-1")
	p.client.Transport = rewriteTransport{target}

	_, err := p.Stream(context.Background(), core.Request{Model: core.Model{ID: "muse-spark-1.3"}, Options: core.StreamOptions{ThinkingLevel: "low"}})
	if _, ok := err.(*core.QuotaExceededError); !ok {
		t.Fatalf("err = %#v, want *core.QuotaExceededError", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestStream_ErrorBodyIsNotSurfaced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"message":"secret-key-abc123 rejected"}}`)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	p := New("key-1")
	p.client.Transport = rewriteTransport{target}
	_, err := p.Stream(context.Background(), core.Request{Model: core.Model{ID: "muse-spark-1.3"}, Options: core.StreamOptions{ThinkingLevel: "low"}})
	if err == nil || strings.Contains(err.Error(), "secret-key-abc123") {
		t.Fatalf("err = %v", err)
	}
}
