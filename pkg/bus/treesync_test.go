package bus

import (
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
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

func TestClearSessionRejectsDeltaBeforeAsyncTreeSync(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.Tree = session.NewTree()
	RegisterHandlers(sctx)

	// Hold the async tree-sync subscriber at the clear event. This is the
	// precise old race: Reset had already emptied the agent, but TreeSyncer had
	// not yet consumed CommandExecuted and its old tree still validated tokens.
	entered := make(chan struct{})
	release := make(chan struct{})
	b.SubscribeAll(func(event any) {
		if command, ok := event.(CommandExecuted); ok && command.Command == "clear" {
			close(entered)
			<-release
		}
	})
	RegisterTreeSyncer(b, sctx)

	fa.mu.Lock()
	fa.messages = []core.AgentMessage{msgWithID("user", "before clear", "old-message")}
	fa.mu.Unlock()
	b.Publish(RunEnded{SessionID: sctx.SessionID})
	b.Drain(time.Second)

	if err := b.Execute(ClearSession{}); err != nil {
		t.Fatal(err)
	}
	<-entered
	defer close(release)

	delta, err := QueryTyped[GetDisplayMessagesSince, DisplayMessagesSince](b, GetDisplayMessagesSince{EntryID: "old-message"})
	if err != nil {
		t.Fatal(err)
	}
	if delta.Valid {
		t.Fatalf("cleared session accepted stale delta token: %+v", delta)
	}

	full, err := QueryTyped[GetDisplayMessages, []core.AgentMessage](b, GetDisplayMessages{})
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 0 {
		t.Fatalf("cleared session display = %#v, want empty", full)
	}
}

// TestTreeSyncer_NoDuplicateUserAcrossCompaction is a syncer-level guard: given
// the ingress invariant (every message carries a stable MsgID — enforced in
// pkg/agent and proven by TestIngress_AllUserMessagesGetMsgID and
// TestCompact_RetainedUserKeepsStableMsgID), the tree syncer must recognize a
// user message retained across a compaction and not re-append it after the
// EntryCompaction marker. This isolates the syncer's dedup contract; the actual
// root cause (anonymous ingress) is guarded in pkg/agent.
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

func TestTreeSyncer_CompactionUsesLiveMarkerID(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.Tree = session.NewTree()
	RegisterHandlers(sctx)
	RegisterTreeSyncer(b, sctx)

	marker := NewCompactionMarker(&core.CompactionPayload{Summary: "summary", TokensBefore: 12000})
	b.Publish(CompactionEnded{SessionID: "test-session", Payload: &core.CompactionPayload{Summary: "summary", TokensBefore: 12000}, Marker: marker})
	b.Drain(time.Second)
	all := sctx.Tree.AllMessages()
	if len(all) != 1 || all[0].MsgID != marker.MsgID {
		t.Fatalf("display marker = %+v, want durable ID %q", all, marker.MsgID)
	}
}

func msgWithID(role, text, id string) core.AgentMessage {
	m := msg(role, text)
	m.MsgID = id
	return m
}

// TestMsgIDInUse covers the query the serve layer uses to refuse a
// client-supplied message ID that is already taken. It must see BOTH the synced
// tree history and the in-flight turn: those are exactly the messages clients
// dedup against, so an ID present in either would swallow a new message.
func TestMsgIDInUse(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.Tree = session.NewTree()
	RegisterHandlers(sctx)
	RegisterTreeSyncer(b, sctx)

	fa.mu.Lock()
	fa.messages = []core.AgentMessage{msgWithID("user", "hi", "m-synced"), msgWithID("assistant", "hello", "m-a")}
	fa.mu.Unlock()
	b.Publish(RunEnded{SessionID: "test-session"})
	b.Drain(time.Second)

	// In-flight turn: on the agent, not yet in the tree.
	fa.mu.Lock()
	fa.messages = append(fa.messages, msgWithID("user", "second", "m-inflight"))
	fa.mu.Unlock()

	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"m-synced", true},
		{"m-inflight", true},
		{"m-free", false},
		{"", false},
	} {
		got, err := QueryTyped[MsgIDInUse, bool](b, MsgIDInUse{MsgID: tc.id})
		if err != nil {
			t.Fatalf("query %q failed: %v", tc.id, err)
		}
		if got != tc.want {
			t.Fatalf("MsgIDInUse(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// TestMsgIDInUse_WithoutTreeSyncer exercises the fallback path (no syncer
// registered): the agent's own messages are still the source of truth.
func TestMsgIDInUse_WithoutTreeSyncer(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	RegisterHandlers(sctx)

	fa.mu.Lock()
	fa.messages = []core.AgentMessage{msgWithID("user", "hi", "m-1")}
	fa.mu.Unlock()

	if got, _ := QueryTyped[MsgIDInUse, bool](b, MsgIDInUse{MsgID: "m-1"}); !got {
		t.Fatal("MsgIDInUse(m-1) = false, want true")
	}
	if got, _ := QueryTyped[MsgIDInUse, bool](b, MsgIDInUse{MsgID: "m-2"}); got {
		t.Fatal("MsgIDInUse(m-2) = true, want false")
	}
}
