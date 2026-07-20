package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/ealeixandre/moa/pkg/sessioncheckpoint"
)

func TestSessionCheckpointToolActionsAndEffect(t *testing.T) {
	slot := sessioncheckpoint.New()
	tool := NewSessionCheckpoint(slot)
	if tool.Effect != 0 {
		t.Fatalf("effect = %v", tool.Effect)
	}
	result, err := tool.Execute(context.Background(), map[string]any{"action": "read"}, nil)
	if err != nil || result.Content[0].Text != "No checkpoint is set." {
		t.Fatalf("empty read = %#v, %v", result, err)
	}
	result, _ = tool.Execute(context.Background(), map[string]any{"action": "write", "content": "handoff"}, nil)
	if result.IsError {
		t.Fatalf("write: %#v", result)
	}
	result, _ = tool.Execute(context.Background(), map[string]any{"action": "read"}, nil)
	if result.Content[0].Text != "handoff" {
		t.Fatalf("read = %q", result.Content[0].Text)
	}
	result, _ = tool.Execute(context.Background(), map[string]any{"action": "write", "content": strings.Repeat("x", sessioncheckpoint.MaxBytes+1)}, nil)
	if !result.IsError {
		t.Fatal("oversized checkpoint accepted")
	}
	result, _ = tool.Execute(context.Background(), map[string]any{"action": "clear"}, nil)
	if result.IsError {
		t.Fatal("clear failed")
	}
}
