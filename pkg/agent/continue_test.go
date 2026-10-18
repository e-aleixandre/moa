package agent

import (
	"context"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Continue answers the message already in history and adds none of its own.
func TestContinueRunsOverTheConversationWithoutAppending(t *testing.T) {
	provider := NewMockProvider(simpleTextResponse("read it"))
	ag := newTestAgent(provider)
	if err := ag.LoadState([]core.AgentMessage{core.WrapMessage(core.NewUserMessage("the answer"))}, 0); err != nil {
		t.Fatal(err)
	}
	msgs, err := ag.Continue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Fatalf("roles after Continue = %v, want [user assistant]", roles(msgs))
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
}

// A conversation is continued from where the loop itself would have gone on:
// input for the model, or a reply the provider said is not the end of the turn.
func TestOwesContinuation(t *testing.T) {
	msg := func(role, stop string) core.AgentMessage {
		m := core.Message{Role: role, StopReason: stop}
		return core.WrapMessage(m)
	}
	cases := []struct {
		name string
		msgs []core.AgentMessage
		want bool
	}{
		{"empty", nil, false},
		{"user", []core.AgentMessage{msg("user", "")}, true},
		{"tool result", []core.AgentMessage{msg("assistant", "tool_use"), msg("tool_result", "")}, true},
		{"final reply", []core.AgentMessage{msg("user", ""), msg("assistant", "end_turn")}, false},
		{"pause_turn", []core.AgentMessage{msg("user", ""), msg("assistant", "pause_turn")}, true},
		{"continue", []core.AgentMessage{msg("user", ""), msg("assistant", "continue")}, true},
		{"truncated reply", []core.AgentMessage{msg("user", ""), msg("assistant", "max_tokens")}, false},
		{"custom entry after a final reply", []core.AgentMessage{msg("assistant", "end_turn"), {Message: core.Message{Role: "compaction"}}}, false},
	}
	for _, c := range cases {
		if got := OwesContinuation(c.msgs); got != c.want {
			t.Errorf("%s: OwesContinuation = %v, want %v", c.name, got, c.want)
		}
	}
}
