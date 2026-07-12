package bus

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

// TestCleanRunError guards the user-facing rendering of run errors: a usage
// limit shows its typed, actionable message, while a generic error is stripped
// of the internal "stream: provider:" plumbing prefixes.
func TestCleanRunError(t *testing.T) {
	quota := &core.QuotaExceededError{Provider: "openai", Message: "The usage limit has been reached"}
	wrapped := fmt.Errorf("stream: %w", fmt.Errorf("provider: %w", quota))
	if got := cleanRunError(wrapped); got != "openai quota exceeded: The usage limit has been reached" {
		t.Fatalf("quota clean = %q", got)
	}
	generic := errors.New("stream: provider: openai: HTTP 500: boom")
	if got := cleanRunError(generic); got != "openai: HTTP 500: boom" {
		t.Fatalf("generic clean = %q", got)
	}
	if got := cleanRunError(nil); got != "" {
		t.Fatalf("nil clean = %q, want empty", got)
	}
}

func TestHasSuccessfulEdits_True(t *testing.T) {
	msgs := []core.AgentMessage{
		core.WrapMessage(core.NewToolResultMessage("tc1", "read", []core.Content{core.TextContent("ok")}, false)),
		core.WrapMessage(core.NewToolResultMessage("tc2", "edit", []core.Content{core.TextContent("ok")}, false)),
	}
	if !hasSuccessfulEdits(msgs) {
		t.Error("expected true: edit tool succeeded")
	}
}

func TestHasSuccessfulEdits_False_NoEdits(t *testing.T) {
	msgs := []core.AgentMessage{
		core.WrapMessage(core.NewToolResultMessage("tc1", "read", []core.Content{core.TextContent("ok")}, false)),
		core.WrapMessage(core.NewToolResultMessage("tc2", "grep", []core.Content{core.TextContent("ok")}, false)),
	}
	if hasSuccessfulEdits(msgs) {
		t.Error("expected false: no edit tools")
	}
}

func TestHasSuccessfulEdits_False_FailedEdit(t *testing.T) {
	msgs := []core.AgentMessage{
		core.WrapMessage(core.NewToolResultMessage("tc1", "edit", []core.Content{core.TextContent("err")}, true)),
	}
	if hasSuccessfulEdits(msgs) {
		t.Error("expected false: edit failed (IsError)")
	}
}

func TestHasSuccessfulEdits_AllEditTools(t *testing.T) {
	for _, tool := range []string{"edit", "write", "multiedit", "apply_patch"} {
		msgs := []core.AgentMessage{
			core.WrapMessage(core.NewToolResultMessage("tc1", tool, []core.Content{core.TextContent("ok")}, false)),
		}
		if !hasSuccessfulEdits(msgs) {
			t.Errorf("expected true for tool %s", tool)
		}
	}
}

func TestHasSuccessfulEdits_Empty(t *testing.T) {
	if hasSuccessfulEdits(nil) {
		t.Error("expected false for empty messages")
	}
}

func TestHasSuccessfulEdits_SkipsAssistantMessages(t *testing.T) {
	msgs := []core.AgentMessage{
		core.WrapMessage(core.Message{
			Role:    "assistant",
			Content: []core.Content{core.ToolCallContent("tc1", "edit", map[string]any{"path": "foo"})},
		}),
	}
	// tool_call messages have role "assistant", not "tool_result" — should be ignored.
	if hasSuccessfulEdits(msgs) {
		t.Error("expected false: assistant messages should be skipped")
	}
}

// TestHandler_BranchTo_RejectsWhileNotIdle verifies BranchTo refuses to run
// while a run is in flight (running) or a permission is pending, and — crucially
// — does NOT mutate the tree's leaf. The permission case regressed: the old
// guard only rejected StateRunning, so a pending permission moved the leaf
// before LoadState failed, leaving the tree inconsistent.
func TestHandler_BranchTo_RejectsWhileNotIdle(t *testing.T) {
	for _, st := range []SessionState{StateRunning, StatePermission} {
		t.Run(string(st), func(t *testing.T) {
			b := NewLocalBus()
			defer b.Close()
			fa := &fakeAgent{}
			sctx := newTestSessionContextWithState(b, fa)

			// Two message entries so there is a valid branch target.
			tree := session.NewTree()
			firstID := tree.Append(session.Entry{
				Type:    session.EntryMessage,
				Message: core.WrapMessage(core.Message{Role: "user", Content: []core.Content{core.TextContent("first")}}),
			})
			tree.Append(session.Entry{
				Type:    session.EntryMessage,
				Message: core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("answer")}}),
			})
			sctx.Tree = tree
			RegisterHandlers(sctx)

			leafBefore := tree.LeafID()
			sctx.State.ForceState(st)

			if err := b.Execute(BranchTo{EntryID: firstID}); err == nil {
				t.Fatalf("expected BranchTo to be rejected in state %s", st)
			}
			if got := tree.LeafID(); got != leafBefore {
				t.Fatalf("tree leaf changed despite rejected branch: %s → %s", leafBefore, got)
			}
		})
	}
}

// TestHandler_BranchTo_AllowedWhenIdle confirms the guard still permits
// branching from a terminal state.
func TestHandler_BranchTo_AllowedWhenIdle(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)

	tree := session.NewTree()
	firstID := tree.Append(session.Entry{
		Type:    session.EntryMessage,
		Message: core.WrapMessage(core.Message{Role: "user", Content: []core.Content{core.TextContent("first")}}),
	})
	tree.Append(session.Entry{
		Type:    session.EntryMessage,
		Message: core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent("answer")}}),
	})
	sctx.Tree = tree
	RegisterHandlers(sctx)

	if err := b.Execute(BranchTo{EntryID: firstID}); err != nil {
		t.Fatalf("BranchTo while idle: %v", err)
	}
	if got := tree.LeafID(); got != firstID {
		t.Fatalf("leaf = %s, want %s", got, firstID)
	}
}

// TestHandler_BranchTo_RejectsDanglingToolCall verifies the F15 guard: an
// assistant turn with an unresolved tool_call must not become the new leaf,
// even while idle, and GetBranchPoints must not offer it as a target.
func TestHandler_BranchTo_RejectsDanglingToolCall(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)

	tree := session.NewTree()
	tree.Append(session.Entry{
		Type:    session.EntryMessage,
		Message: core.WrapMessage(core.Message{Role: "user", Content: []core.Content{core.TextContent("run a command")}}),
	})
	acID := tree.Append(session.Entry{
		Type: session.EntryMessage,
		Message: core.WrapMessage(core.Message{
			Role:    "assistant",
			Content: []core.Content{core.ToolCallContent("tc1", "bash", map[string]any{"command": "echo hi"})},
		}),
	})
	sctx.Tree = tree
	RegisterHandlers(sctx)

	leafBefore := tree.LeafID()
	if err := b.Execute(BranchTo{EntryID: acID}); err == nil {
		t.Fatal("expected BranchTo to reject an assistant entry with an unresolved tool call")
	}
	if got := tree.LeafID(); got != leafBefore {
		t.Fatalf("tree leaf changed despite rejected branch: %s → %s", leafBefore, got)
	}

	points, err := QueryTyped[GetBranchPoints, []BranchPoint](b, GetBranchPoints{})
	if err != nil {
		t.Fatalf("GetBranchPoints: %v", err)
	}
	for _, p := range points {
		if p.EntryID == acID {
			t.Fatalf("GetBranchPoints must not offer an entry with a dangling tool call: %+v", p)
		}
	}
}
