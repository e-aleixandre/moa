package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The ChatGPT backend keys a conversation's prompt cache off the session-id
// header. Sending only prompt_cache_key in the body makes every turn look like
// a new session: the prefix is written each time and never read back.
func TestStream_OAuthSendsSessionIDHeader(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseEvent(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-sol","status":"completed","usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}`))) //nolint:errcheck
	}))
	defer server.Close()

	provider := NewOAuth("tok", "acct-1", nil)
	provider.baseURL = server.URL
	provider.endpoint = codexEndpoint

	req := core.Request{
		Model:    core.Model{ID: "gpt-daybreak-blue-latest", Provider: "openai"},
		Messages: []core.Message{{Role: "user", Content: []core.Content{{Type: "text", Text: "hi"}}}},
		Options:  core.StreamOptions{PromptCacheKey: "moa:session:abc123"},
	}
	ch, err := provider.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch { //nolint:revive
	}

	if v := got.Get("session-id"); v != "moa:session:abc123" {
		t.Errorf("session-id = %q, want the request's prompt cache key", v)
	}
}

// An empty key must not become an empty header: that would lump every
// unidentified conversation into one session server-side.
func TestStream_OAuthOmitsEmptySessionID(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseEvent(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-sol","status":"completed","usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}`))) //nolint:errcheck
	}))
	defer server.Close()

	provider := NewOAuth("tok", "acct-1", nil)
	provider.baseURL = server.URL
	provider.endpoint = codexEndpoint

	req := core.Request{
		Model:    core.Model{ID: "gpt-5.6-terra", Provider: "openai"},
		Messages: []core.Message{{Role: "user", Content: []core.Content{{Type: "text", Text: "hi"}}}},
	}
	ch, err := provider.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch { //nolint:revive
	}

	if _, ok := got["Session-Id"]; ok {
		t.Errorf("session-id was sent without a key: %q", got.Get("session-id"))
	}
}

// The header identifies the ChatGPT backend's session. API-key requests go to
// api.openai.com, which never asked for it.
func TestStream_APIKeyPathOmitsSessionID(t *testing.T) {
	var got http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseEvent(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.6-sol","status":"completed","usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}`))) //nolint:errcheck
	}))
	defer server.Close()

	provider := NewWithBaseURL("sk-test", server.URL)
	req := core.Request{
		Model:    core.Model{ID: "gpt-5.6-sol", Provider: "openai"},
		Messages: []core.Message{{Role: "user", Content: []core.Content{{Type: "text", Text: "hi"}}}},
		Options:  core.StreamOptions{PromptCacheKey: "moa:session:abc123"},
	}
	ch, err := provider.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch { //nolint:revive
	}

	if _, ok := got["Session-Id"]; ok {
		t.Errorf("api-key path sent session-id: %q", got.Get("session-id"))
	}
}
