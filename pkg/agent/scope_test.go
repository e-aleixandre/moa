package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/core"
)

const (
	testOwnerSession  = "0123456789abcdef01234567"
	testParentSession = "89abcdef0123456789abcdef"
)

func newTestScope(t *testing.T, sessionID string) *attachment.Scope {
	t.Helper()
	store, err := attachment.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := attachment.NewScope(store, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

// scopeProbeTool records the attachment scope visible in the context of each
// tool invocation, which is exactly what pkg/tool and the MCP wrappers will
// read per invocation (never captured at construction time).
func scopeProbeTool(seen *[]*attachment.Scope) core.Tool {
	return core.Tool{
		Name:       "probe",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			*seen = append(*seen, attachment.ScopeFromContext(ctx))
			return core.TextResult("ok"), nil
		},
	}
}

// TestAgentPublishesItsScopeToTools checks the producing half: a tool invoked
// by an agent that holds the capability sees exactly that capability.
func TestAgentPublishesItsScopeToTools(t *testing.T) {
	scope := newTestScope(t, testOwnerSession)
	var seen []*attachment.Scope
	reg := core.NewRegistry()
	if err := reg.Register(scopeProbeTool(&seen)); err != nil {
		t.Fatal(err)
	}
	provider := NewMockProvider(
		toolCallResponse("tc-1", "probe", nil),
		simpleTextResponse("done"),
	)
	ag, err := New(AgentConfig{
		Provider:        provider,
		Model:           core.Model{ID: "test-model", Provider: "mock"},
		Tools:           reg,
		AttachmentScope: scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("probe ran %d times, want 1", len(seen))
	}
	if seen[0] != scope {
		t.Fatalf("tool saw scope %v, want the agent's own scope %v", seen[0], scope)
	}
}

// TestNilScopeShadowsInheritedScope is the test that forbids "optimizing"
// executeWithOptions into `if scope != nil`. An ephemeral agent (plan/code
// reviewer, goal verifier) runs on a context inherited from its parent's tool
// call, which already carries the parent's capability. Configured with nil, it
// must see NO capability at all — otherwise it would externalize into the
// parent's index and leave orphan references behind.
func TestNilScopeShadowsInheritedScope(t *testing.T) {
	parentScope := newTestScope(t, testParentSession)
	parentCtx := attachment.WithScope(context.Background(), parentScope)

	var seen []*attachment.Scope
	reg := core.NewRegistry()
	// Deliberately the SAME tool value a parent would have registered: the
	// capability must be resolved per invocation, never captured.
	if err := reg.Register(scopeProbeTool(&seen)); err != nil {
		t.Fatal(err)
	}
	provider := NewMockProvider(
		toolCallResponse("tc-1", "probe", nil),
		simpleTextResponse("done"),
	)
	ephemeral, err := New(AgentConfig{
		Provider:        provider,
		Model:           core.Model{ID: "test-model", Provider: "mock"},
		Tools:           reg,
		AttachmentScope: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ephemeral.Run(parentCtx, "review"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 {
		t.Fatalf("probe ran %d times, want 1", len(seen))
	}
	if seen[0] != nil {
		t.Fatalf("ephemeral agent leaked an inherited scope (%v) to its tools: writing nil must shadow it", seen[0])
	}
}

// TestScopeSuppliesMaterializerEvenWithoutMaterializeContent checks the
// consuming half: holding a scope is enough to resolve references, no separate
// hook required. Producer and resolver come from the same value.
func TestScopeSuppliesMaterializerEvenWithoutMaterializeContent(t *testing.T) {
	scope := newTestScope(t, testOwnerSession)
	descriptor, err := scope.Put([]byte("real image bytes"), attachment.PutMeta{
		Name: "photo.png",
		Mime: "image/png",
		Kind: "image",
	})
	if err != nil {
		t.Fatal(err)
	}

	provider := NewMockProvider(func(req core.Request) (<-chan core.AssistantEvent, error) {
		if got := req.Messages[0].Content[0].Data; got == "" {
			t.Error("provider received an empty image: the scope's materializer did not run")
		}
		return simpleTextResponse("done")(req)
	})
	ag, err := New(AgentConfig{
		Provider:        provider,
		Model:           core.Model{ID: "test-model", Provider: "mock"},
		AttachmentScope: scope,
		// No MaterializeContent on purpose.
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.SendWithContent(context.Background(), []core.Content{{
		Type:         "image",
		AttachmentID: descriptor.ID,
		MimeType:     "image/png",
	}}); err != nil {
		t.Fatal(err)
	}
}

// TestMaterializeContentCannotDisableScopeMaterializer: a caller-provided hook
// may transform messages, but it must never REPLACE the scope's materializer —
// that would give an agent the producer without the resolver.
func TestMaterializeContentCannotDisableScopeMaterializer(t *testing.T) {
	scope := newTestScope(t, testOwnerSession)
	descriptor, err := scope.Put([]byte("real image bytes"), attachment.PutMeta{
		Name: "photo.png",
		Mime: "image/png",
		Kind: "image",
	})
	if err != nil {
		t.Fatal(err)
	}

	provider := NewMockProvider(func(req core.Request) (<-chan core.AssistantEvent, error) {
		if got := req.Messages[0].Content[0].Data; got == "" {
			t.Error("provider received an empty image: a custom MaterializeContent disabled the scope's materializer")
		}
		return simpleTextResponse("done")(req)
	})
	ag, err := New(AgentConfig{
		Provider: provider,
		Model:    core.Model{ID: "test-model", Provider: "mock"},
		// A pass-through hook that resolves nothing: on its own it would send
		// the descriptor with no bytes.
		MaterializeContent: func(_ context.Context, msgs []core.Message) ([]core.Message, error) {
			return msgs, nil
		},
		AttachmentScope: scope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.SendWithContent(context.Background(), []core.Content{{
		Type:         "image",
		AttachmentID: descriptor.ID,
		MimeType:     "image/png",
	}}); err != nil {
		t.Fatal(err)
	}
}
