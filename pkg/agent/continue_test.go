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
