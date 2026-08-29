package subagent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/e-aleixandre/moa/pkg/attachment"
	"github.com/e-aleixandre/moa/pkg/core"
)

// parentSessionID is a valid attachment owner ID for the PARENT session.
const parentSessionID = "0123456789abcdef01234567"

// TestChildAgentCarriesParentScopeFromConfig pins Rule 2 of the design: a child
// cannot inherit the capability from the spawning context (its context is
// derived from cfg.AppCtx, not from the parent's tool call), so it must receive
// it through its own AgentConfig — owned by the PARENT session, never the job
// ID. The child here runs on a bare context precisely to prove nothing is
// inherited.
func TestChildAgentCarriesParentScopeFromConfig(t *testing.T) {
	store, err := attachment.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parentScope, err := attachment.NewScope(store, parentSessionID)
	if err != nil {
		t.Fatal(err)
	}

	var seen []*attachment.Scope
	reg := core.NewRegistry()
	if err := reg.Register(core.Tool{
		Name:       "probe",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			seen = append(seen, attachment.ScopeFromContext(ctx))
			return core.TextResult("ok"), nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	provider := newMockProvider(
		toolCallResponse("tc-1", "probe", map[string]any{}),
		textResponse("done"),
	)
	child, err := newChildAgent(
		Config{AttachmentScope: parentScope}, provider,
		core.Model{ID: "m", Provider: "mock"}, "medium", 0, "sys", reg, "job-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.Run(context.Background(), "task"); err != nil {
		t.Fatal(err)
	}

	if len(seen) != 1 {
		t.Fatalf("probe ran %d times, want 1", len(seen))
	}
	if seen[0] == nil {
		t.Fatal("child tool saw no attachment scope: the parent capability did not travel through the child's AgentConfig")
	}
	if got := seen[0].SessionID(); got != parentSessionID {
		t.Fatalf("child scope owner = %q, want the parent session %q (never the job ID)", got, parentSessionID)
	}
}

// TestChildAgentWithoutParentScopeWorksInline: no capability configured means
// children work inline, exactly as before this change.
func TestChildAgentWithoutParentScopeWorksInline(t *testing.T) {
	var seen []*attachment.Scope
	reg := core.NewRegistry()
	if err := reg.Register(core.Tool{
		Name:       "probe",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(ctx context.Context, _ map[string]any, _ func(core.Result)) (core.Result, error) {
			seen = append(seen, attachment.ScopeFromContext(ctx))
			return core.TextResult("ok"), nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	provider := newMockProvider(
		toolCallResponse("tc-1", "probe", map[string]any{}),
		textResponse("done"),
	)
	child, err := newChildAgent(
		Config{}, provider, core.Model{ID: "m", Provider: "mock"}, "medium", 0, "sys", reg, "job-test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.Run(context.Background(), "task"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != nil {
		t.Fatalf("child tool scopes = %#v, want exactly one nil scope", seen)
	}
}
