package subagent

import (
	"context"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// TestChildAgentUsesOwnCacheKey: the child is pinned to the session's
// subagent group, not the parent's key and not a per-job suffix.
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

	if want := "moa:session:sess-abc:subagent"; seen != want {
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
