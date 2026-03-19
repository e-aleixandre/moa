package bus

import (
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

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
