package core

import "testing"

// TestPromptCacheKey covers the conversation identifier sent to the Responses
// providers for cache routing.
func TestPromptCacheKey(t *testing.T) {
	if got, want := PromptCacheKey("abc123"), "moa:session:abc123"; got != want {
		t.Errorf("PromptCacheKey = %q, want %q", got, want)
	}
	// No session means no key: callers omit the field rather than sending an
	// empty string, which would group unrelated requests together.
	if got := PromptCacheKey(""); got != "" {
		t.Errorf("empty session id produced %q, want empty", got)
	}
}

// TestSubagentPromptCacheKey: siblings share a routing group (same
// instructions+tools prefix), still distinct from the parent.
func TestSubagentPromptCacheKey(t *testing.T) {
	parent := PromptCacheKey("abc123")
	child := SubagentPromptCacheKey(parent)
	if want := "moa:session:abc123:subagent"; child != want {
		t.Errorf("child key = %q, want %q", child, want)
	}
	if child == parent {
		t.Error("child must not share the parent's routing key")
	}
	if other := SubagentPromptCacheKey(parent); other != child {
		t.Errorf("siblings must share a key, got %q vs %q", child, other)
	}
	if got := SubagentPromptCacheKey(""); got != "" {
		t.Errorf("keyless parent produced %q, want empty", got)
	}
}
