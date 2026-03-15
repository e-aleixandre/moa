package bootstrap

import (
	"context"
	"testing"

	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/session"
)

type mockProvider struct{}

func (m *mockProvider) Stream(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
	ch := make(chan core.AssistantEvent)
	close(ch)
	return ch, nil
}

func newTestBootstrapSession(thinking string) *Session {
	model := core.Model{ID: "test-model", Provider: "test"}
	ag, err := agent.New(agent.AgentConfig{
		Provider:      &mockProvider{},
		Model:         model,
		ThinkingLevel: thinking,
	})
	if err != nil {
		panic(err)
	}
	return &Session{Agent: ag, Model: model}
}

func TestRestoreFromMetadata_Thinking(t *testing.T) {
	bs := newTestBootstrapSession("medium")
	sess := &session.Session{}
	sess.SetRuntimeMetadata("", "", "", "high")

	result := bs.RestoreFromMetadata(sess, nil)
	if result.Thinking != "high" {
		t.Errorf("thinking = %q, want high", result.Thinking)
	}
	if bs.Agent.ThinkingLevel() != "high" {
		t.Errorf("agent thinking = %q, want high", bs.Agent.ThinkingLevel())
	}
}

func TestRestoreFromMetadata_PermissionAsk(t *testing.T) {
	bs := newTestBootstrapSession("medium")
	sess := &session.Session{}
	sess.SetRuntimeMetadata("", "", "ask", "")

	result := bs.RestoreFromMetadata(sess, nil)
	if result.PermissionMode != "ask" {
		t.Errorf("permission mode = %q, want ask", result.PermissionMode)
	}
	if bs.Gate == nil || bs.Gate.Mode() != permission.ModeAsk {
		t.Error("gate should be in ask mode")
	}
}

func TestRestoreFromMetadata_Yolo(t *testing.T) {
	bs := newTestBootstrapSession("medium")
	bs.Gate = permission.New(permission.ModeAsk, permission.Config{})

	sess := &session.Session{}
	sess.SetRuntimeMetadata("", "", "yolo", "")

	result := bs.RestoreFromMetadata(sess, nil)
	if result.PermissionMode != "yolo" {
		t.Errorf("permission mode = %q, want yolo", result.PermissionMode)
	}
	if bs.Gate != nil {
		t.Error("gate should be nil after restoring yolo")
	}
}

func TestRestoreFromMetadata_NilSession(t *testing.T) {
	bs := newTestBootstrapSession("medium")
	result := bs.RestoreFromMetadata(nil, nil)
	if result.PermissionMode != "yolo" {
		t.Errorf("permission mode = %q, want yolo", result.PermissionMode)
	}
}

func TestRestoreFromMetadata_InvalidThinking(t *testing.T) {
	bs := newTestBootstrapSession("medium")
	sess := &session.Session{}
	sess.SetRuntimeMetadata("", "", "", "invalid")

	result := bs.RestoreFromMetadata(sess, nil)
	if result.Thinking != "" {
		t.Errorf("thinking = %q, want empty (invalid ignored)", result.Thinking)
	}
	if bs.Agent.ThinkingLevel() != "medium" {
		t.Errorf("agent thinking should remain medium, got %q", bs.Agent.ThinkingLevel())
	}
}

func TestFullModelSpec(t *testing.T) {
	tests := []struct {
		model core.Model
		want  string
	}{
		{core.Model{ID: "claude-sonnet-4", Provider: "anthropic"}, "anthropic/claude-sonnet-4"},
		{core.Model{ID: "gpt-4o"}, "gpt-4o"},
	}
	for _, tt := range tests {
		got := FullModelSpec(tt.model)
		if got != tt.want {
			t.Errorf("FullModelSpec(%+v) = %q, want %q", tt.model, got, tt.want)
		}
	}
}
