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

// TestSubagentPromptCacheKey locks in that a child gets its OWN routing group.
// A child has its own system prompt, tools and history, so it shares no
// reusable prefix with its parent; routing both to the same machine would
// concentrate unrelated traffic without any chance of a cache hit.
func TestSubagentPromptCacheKey(t *testing.T) {
	parent := PromptCacheKey("abc123")
	child := SubagentPromptCacheKey(parent, "sa-deadbeef")
	if want := "moa:session:abc123:subagent:sa-deadbeef"; child != want {
		t.Errorf("child key = %q, want %q", child, want)
	}
	if child == parent {
		t.Error("child must not share the parent's routing key")
	}
	// Two children of the same parent are separate conversations too.
	if other := SubagentPromptCacheKey(parent, "sa-cafe"); other == child {
		t.Error("two jobs produced the same key")
	}
	// Without a parent key (no session) or a job id there is nothing to pin.
	if got := SubagentPromptCacheKey("", "sa-deadbeef"); got != "" {
		t.Errorf("keyless parent produced %q, want empty", got)
	}
	if got := SubagentPromptCacheKey(parent, ""); got != "" {
		t.Errorf("missing job id produced %q, want empty", got)
	}
}
