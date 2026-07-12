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

// TestTreeSyncer_NoDuplicateUserAcrossCompaction is a syncer-level guard: given
// the ingress invariant (every message carries a stable MsgID — enforced in
// pkg/agent and proven by TestIngress_AllUserMessagesGetMsgID and
// TestCompact_RetainedUserKeepsStableMsgID), the tree syncer must recognize a
// user message retained across a compaction and not re-append it after the
// EntryCompaction marker. This isolates the syncer's dedup contract; the actual
// bug #13 root cause (anonymous ingress) is guarded in pkg/agent.
func TestTreeSyncer_NoDuplicateUserAcrossCompaction(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.Tree = session.NewTree()
	RegisterHandlers(sctx)
	RegisterTreeSyncer(b, sctx)

	// Turn 1: user "keep me" + assistant reply, both carrying stable MsgIDs
	// (as the fixed ingress guarantees). Sync to the tree.
	keep := msg("user", "keep me")
	keep.MsgID = "u-keep"
	reply := msg("assistant", "sure")
	reply.MsgID = "a-reply"
	fa.mu.Lock()
	fa.messages = []core.AgentMessage{keep, reply}
	fa.mu.Unlock()
	b.Publish(RunEnded{SessionID: "test-session"})
	b.Drain(time.Second)

	// Compaction retains the user message. The agent state after compaction is
	// [summary, keep, reply]; the tree records only the compaction marker.
	fa.mu.Lock()
	summary := msg("assistant", "summary")
	summary.MsgID = "sum-1"
	fa.messages = []core.AgentMessage{summary, keep, reply}
	fa.mu.Unlock()
	b.Publish(CompactionEnded{
		SessionID: "test-session",
		Payload: &core.CompactionPayload{
			Summary:        "summary",
			SummaryMsgID:   "sum-1",
			FirstKeptMsgID: "u-keep",
		},
	})
	b.Drain(time.Second)

	// Next turn ends -> syncMessages runs over the compacted state.
	b.Publish(RunEnded{SessionID: "test-session"})
	b.Drain(time.Second)

	all := sctx.Tree.AllMessages()
	count := 0
	for _, m := range all {
		if m.MsgID == "u-keep" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("retained user message appears %d times, want 1 (duplicated after compaction)", count)
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
