package responses

import (
	"encoding/json"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// decodeBody builds a request body and returns it as a generic map, so a test
// can assert on the presence of a field as well as its value.
func decodeBody(t *testing.T, req core.Request, dialect Dialect) map[string]any {
	t.Helper()
	raw, err := BuildRequestBody(req, dialect)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// TestBuildRequestBody_PromptCacheKey covers the routing key OpenAI and xAI use
// to send requests sharing a prefix to the machine holding their cache.
func TestBuildRequestBody_PromptCacheKey(t *testing.T) {
	req := core.Request{
		Model:   core.Model{ID: "gpt-5.6-terra"},
		System:  "sys",
		Options: core.StreamOptions{PromptCacheKey: "moa:session:abc123"},
	}

	// Sent when the transport supports it.
	body := decodeBody(t, req, Dialect{Provider: "openai", SupportsPromptCacheKey: true})
	if got := body["prompt_cache_key"]; got != "moa:session:abc123" {
		t.Errorf("prompt_cache_key = %v, want moa:session:abc123", got)
	}

	// Withheld on a transport that has not been verified to accept it, even
	// though the key is present in the request.
	body = decodeBody(t, req, Dialect{Provider: "xai"})
	if _, ok := body["prompt_cache_key"]; ok {
		t.Errorf("unsupported dialect sent prompt_cache_key: %v", body["prompt_cache_key"])
	}

	// Omitted entirely when there is no key, rather than sent empty: an empty
	// string would group every unidentified request into one routing bucket.
	noKey := core.Request{Model: core.Model{ID: "gpt-5.6-terra"}, System: "sys"}
	body = decodeBody(t, noKey, Dialect{Provider: "openai", SupportsPromptCacheKey: true})
	if _, ok := body["prompt_cache_key"]; ok {
		t.Errorf("empty key was serialized: %v", body["prompt_cache_key"])
	}
}
