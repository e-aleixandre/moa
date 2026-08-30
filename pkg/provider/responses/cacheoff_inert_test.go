package responses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// CacheOff is an Anthropic-only billing hint. The Responses providers must
// ignore it: it must never leak into the body or suppress prompt_cache_key,
// which is routing, not caching.
func TestCacheOffIsInertForResponsesProviders(t *testing.T) {
	req := core.Request{
		Model:    core.Model{ID: "gpt-5.6-sol"},
		System:   "summarize",
		Messages: []core.Message{core.NewUserMessage("hi")},
		Options:  core.StreamOptions{CacheRetention: core.CacheOff, PromptCacheKey: "sess-1"},
	}
	data, err := BuildRequestBody(req, Dialect{Provider: "openai", Model: req.Model.ID, SupportsPromptCacheKey: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "cache_retention") || strings.Contains(string(data), `"off"`) {
		t.Fatalf("CacheOff leaked into the Responses body: %s", data)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	if body["prompt_cache_key"] != "sess-1" {
		t.Fatalf("prompt_cache_key = %v, want sess-1: routing must survive CacheOff", body["prompt_cache_key"])
	}
}
