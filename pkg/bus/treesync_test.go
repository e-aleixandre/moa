package bus

import (
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

func msg(role, text string) core.AgentMessage {
	return core.AgentMessage{Message: core.Message{
		Role:    role,
		Content: []core.Content{core.TextContent(text)},
	}}
}

// TestDisplayMessages_IncludesInFlightTurn is the regression guard for
// disappearing messages on a mid-run WS reconnect. The tree only gains a turn's
// messages after RunEnded; DisplayMessages must fold in the un-synced agent
// tail so the reconnect snapshot still shows the just-sent user message and the
// streaming reply that has landed so far.
func TestDisplayMessages_IncludesInFlightTurn(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.Tree = session.NewTree()
	RegisterHandlers(sctx)
	RegisterTreeSyncer(b, sctx)

	// First turn completes and syncs to the tree.
	fa.mu.Lock()
	fa.messages = []core.AgentMessage{msg("user", "hi"), msg("assistant", "hello")}
	fa.mu.Unlock()
	b.Publish(RunEnded{SessionID: "test-session"})
	b.Drain(time.Second)

	// Second turn is in flight: the user message and a partial reply are on the
	// agent but NOT yet synced to the tree (RunEnded hasn't fired).
	fa.mu.Lock()
	fa.messages = append(fa.messages, msg("user", "second"), msg("assistant", "wor"))
	fa.mu.Unlock()

	got, err := QueryTyped[GetDisplayMessages, []core.AgentMessage](b, GetDisplayMessages{})
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("display messages = %d, want 4 (2 synced + 2 in-flight)", len(got))
	}
	if txt := messageText(got[2]); txt != "second" {
		t.Fatalf("got[2] = %q, want the in-flight user message %q", txt, "second")
	}
	if txt := messageText(got[3]); txt != "wor" {
		t.Fatalf("got[3] = %q, want the in-flight partial reply %q", txt, "wor")
	}
}

// TestDisplayMessages_NoDuplicateAfterSync verifies the tail folds away once the
// turn is synced: no message appears twice across the RunEnded boundary.
func TestDisplayMessages_NoDuplicateAfterSync(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.Tree = session.NewTree()
	RegisterHandlers(sctx)
	RegisterTreeSyncer(b, sctx)

	fa.mu.Lock()
	fa.messages = []core.AgentMessage{msg("user", "hi"), msg("assistant", "hello")}
	fa.mu.Unlock()
	b.Publish(RunEnded{SessionID: "test-session"})
	b.Drain(time.Second)

	got, err := QueryTyped[GetDisplayMessages, []core.AgentMessage](b, GetDisplayMessages{})
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("display messages = %d, want 2 (no duplication after sync)", len(got))
	}
}
