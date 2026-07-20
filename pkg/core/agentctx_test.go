package core

import (
	"context"
	"testing"
)

func TestAgentIDContext(t *testing.T) {
	// No tag → "".
	if got := AgentIDFromContext(context.Background()); got != "" {
		t.Fatalf("untagged ctx: got %q, want \"\"", got)
	}

	// Round-trip a value.
	ctx := WithAgentID(context.Background(), "child-42")
	if got := AgentIDFromContext(ctx); got != "child-42" {
		t.Fatalf("round-trip: got %q, want child-42", got)
	}

	// Nesting: the innermost tag wins.
	ctx2 := WithAgentID(ctx, "grandchild-7")
	if got := AgentIDFromContext(ctx2); got != "grandchild-7" {
		t.Fatalf("nested: got %q, want grandchild-7", got)
	}
	// The parent ctx is unchanged.
	if got := AgentIDFromContext(ctx); got != "child-42" {
		t.Fatalf("parent ctx mutated: got %q, want child-42", got)
	}
}

func TestToolCallIDContext(t *testing.T) {
	if got := ToolCallIDFromContext(context.Background()); got != "" {
		t.Fatalf("untagged ctx: got %q, want empty", got)
	}

	ctx := WithToolCallID(context.Background(), "toolu_123")
	if got := ToolCallIDFromContext(ctx); got != "toolu_123" {
		t.Fatalf("round-trip: got %q, want toolu_123", got)
	}
}
