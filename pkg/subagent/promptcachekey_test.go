package subagent

import (
	"context"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// TestChildAgentUsesOwnCacheKey verifies a child's requests carry a routing key
// derived from the parent's plus its job id, never the parent's own.
//
// A child is a separate conversation — its own system prompt, tools and history
// — so it shares no reusable prefix with its parent. Routing both to the same
// machine would concentrate unrelated traffic with no chance of a cache hit.
func TestChildAgentUsesOwnCacheKey(t *testing.T) {
	parentKey := core.PromptCacheKey("sess-abc")

	var seen string
	provider := newMockProvider(func(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		seen = req.Options.PromptCacheKey
		return textResponse("done")(context.Background(), req)
	})

	child, err := newChildAgent(
		Config{PromptCacheKey: parentKey}, provider,
		core.Model{ID: "m", Provider: "mock"}, "medium", 0, "sys", core.NewRegistry(), "sa-999",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.Run(context.Background(), "task"); err != nil {
		t.Fatal(err)
	}

	if want := "moa:session:sess-abc:subagent:sa-999"; seen != want {
		t.Errorf("child request key = %q, want %q", seen, want)
	}
	if seen == parentKey {
		t.Error("child reused the parent's routing key")
	}
}

// TestChildAgentWithoutParentKey covers a parent that has no session behind it:
// the child must send no key rather than a malformed one.
func TestChildAgentWithoutParentKey(t *testing.T) {
	var seen string
	provider := newMockProvider(func(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
		seen = req.Options.PromptCacheKey
		return textResponse("done")(context.Background(), req)
	})
	child, err := newChildAgent(
		Config{}, provider,
		core.Model{ID: "m", Provider: "mock"}, "medium", 0, "sys", core.NewRegistry(), "sa-999",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.Run(context.Background(), "task"); err != nil {
		t.Fatal(err)
	}
	if seen != "" {
		t.Errorf("key = %q, want empty", seen)
	}
}
