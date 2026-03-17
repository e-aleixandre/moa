package bootstrap

import (
	"context"
	"testing"

	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/core"
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

func TestCurrentPermissionMode_NoGate(t *testing.T) {
	bs := newTestBootstrapSession("medium")
	if got := bs.CurrentPermissionMode(); got != "yolo" {
		t.Errorf("CurrentPermissionMode() = %q, want yolo", got)
	}
}
