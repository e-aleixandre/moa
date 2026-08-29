package agent

import (
	"context"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// TestPromptCacheKeyReachesProvider is the end-to-end guard for cache routing:
// the key configured on the agent must arrive in every request it makes. The
// unit tests around it only prove each hop in isolation, and a dropped
// assignment anywhere between AgentConfig and StreamOptions would silently
// disable routing without failing anything else.
func TestPromptCacheKeyReachesProvider(t *testing.T) {
	var seen []string
	provider := NewMockProvider(
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			seen = append(seen, req.Options.PromptCacheKey)
			return simpleTextResponse("done")(req)
		},
	)

	ag, err := New(AgentConfig{
		Provider:       provider,
		Model:          core.Model{ID: "test-model", Provider: "mock"},
		SystemPrompt:   "You are a test agent.",
		PromptCacheKey: "moa:session:abc123",
		Tools:          core.NewRegistry(),
		MaxTurns:       5,
		MaxRunDuration: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}

	if len(seen) == 0 {
		t.Fatal("provider was never called")
	}
	for i, got := range seen {
		if got != "moa:session:abc123" {
			t.Errorf("call %d: PromptCacheKey = %q, want moa:session:abc123", i, got)
		}
	}
}

// TestPromptCacheKeyAbsentByDefault covers an agent with no session behind it
// (one-off helpers): it must send no key rather than an empty one.
func TestPromptCacheKeyAbsentByDefault(t *testing.T) {
	var seen []string
	provider := NewMockProvider(
		func(req core.Request) (<-chan core.AssistantEvent, error) {
			seen = append(seen, req.Options.PromptCacheKey)
			return simpleTextResponse("done")(req)
		},
	)
	ag := newTestAgent(provider)
	if _, err := ag.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 {
		t.Fatal("provider was never called")
	}
	if seen[0] != "" {
		t.Errorf("PromptCacheKey = %q, want empty", seen[0])
	}
}
