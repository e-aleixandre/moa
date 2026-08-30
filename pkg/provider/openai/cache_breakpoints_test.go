package openai

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

func TestModelSupportsExplicitCacheBreakpoints(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"gpt-5.6-terra", true},
		{"gpt-5.6-sol", true},
		{"gpt-5.6-luna", true},
		{"openai/gpt-5.6-sol", true},
		{"GPT-5.6-TERRA", true},
		{"gpt-5.7-preview", true},
		{"gpt-6", true},
		{"gpt-5.5", false},
		{"gpt-5.4-mini", false},
		{"gpt-5.3-codex", false},
		{"gpt-5", false},
		{"gpt-4.1", false},
		{"grok-4.6", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := modelSupportsExplicitCacheBreakpoints(tt.id); got != tt.want {
			t.Errorf("modelSupportsExplicitCacheBreakpoints(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}

func TestStream_GPT56SendsCacheBreakpoints(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseEvent(`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`)))
	}))
	defer server.Close()

	prov := NewWithBaseURL("test-key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.6-terra"},
		System:   "stable instructions",
		Messages: []core.Message{core.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}

	if _, ok := body["instructions"]; ok {
		t.Fatalf("instructions stayed top-level: %v", body["instructions"])
	}
	opts, _ := body["prompt_cache_options"].(map[string]any)
	if opts["mode"] != "implicit" {
		t.Fatalf("prompt_cache_options.mode = %v, want implicit", opts["mode"])
	}
	input, _ := body["input"].([]any)
	if len(input) < 2 {
		t.Fatalf("input len = %d, want developer + user", len(input))
	}
	dev, _ := input[0].(map[string]any)
	if dev["role"] != "developer" {
		t.Fatalf("first input role = %v, want developer", dev["role"])
	}
}

func TestStream_GPT55OmitsCacheBreakpoints(t *testing.T) {
	var raw []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		raw, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sseEvent(`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`)))
	}))
	defer server.Close()

	prov := NewWithBaseURL("test-key", server.URL)
	ch, err := prov.Stream(context.Background(), core.Request{
		Model:    core.Model{ID: "gpt-5.5"},
		System:   "stable instructions",
		Messages: []core.Message{core.NewUserMessage("hello")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}

	if strings.Contains(string(raw), "prompt_cache_breakpoint") || strings.Contains(string(raw), "prompt_cache_options") {
		t.Fatalf("GPT-5.5 body leaked explicit cache fields: %s", raw)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["instructions"] != "stable instructions" {
		t.Fatalf("instructions = %v, want top-level system prompt", body["instructions"])
	}
}
