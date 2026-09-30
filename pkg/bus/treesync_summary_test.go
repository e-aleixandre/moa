package bus

import (
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
)

func ordinarySummaryEntries(entries []session.Entry) int {
	n := 0
	for _, e := range entries {
		if e.Type == session.EntryMessage && e.Message.Role == "compaction_summary" {
			n++
		}
	}
	return n
}

// A branch rehydrates the agent from BuildContext. When the new path ends in a
// compaction the syncer never saw at start (here, one made live in this
// runtime), the rebuilt summary must still be recognised as the boundary it
// comes from and not persisted as a copy by the syncs that follow (the branch
// command's own and the next run's).
func TestTreeSyncer_BranchDoesNotPersistRebuiltSummary(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	fa := &fakeAgent{}
	sctx := newTestSessionContext(b, fa)
	sctx.Tree = session.NewTree()
	RegisterHandlers(sctx)
	RegisterTreeSyncer(b, sctx)

	fa.mu.Lock()
	fa.messages = []core.AgentMessage{msgWithID("user", "old", "u-old"), msgWithID("assistant", "kept", "a-kept")}
	fa.mu.Unlock()
	b.Publish(RunEnded{SessionID: sctx.SessionID})
	b.Drain(time.Second)

	fa.mu.Lock()
	fa.messages = []core.AgentMessage{msgWithID("compaction_summary", "summary", "sum-live"), msgWithID("assistant", "kept", "a-kept")}
	fa.mu.Unlock()
	b.Publish(CompactionEnded{
		SessionID: sctx.SessionID,
		Payload:   &core.CompactionPayload{Summary: "summary", SummaryMsgID: "sum-live", FirstKeptMsgID: "a-kept"},
		Marker:    NewCompactionMarker(&core.CompactionPayload{Summary: "summary"}),
	})
	b.Drain(time.Second)

	fa.mu.Lock()
	fa.messages = append(fa.messages, msgWithID("user", "next", "u-next"))
	fa.mu.Unlock()
	b.Publish(RunEnded{SessionID: sctx.SessionID})
	b.Drain(time.Second)
	entriesBefore := sctx.Tree.Len()

	if err := b.Execute(BranchTo{EntryID: "u-next"}); err != nil {
		t.Fatal(err)
	}
	b.Drain(time.Second)
	b.Publish(RunEnded{SessionID: sctx.SessionID})
	b.Drain(time.Second)

	if got := ordinarySummaryEntries(sctx.Tree.Entries()); got != 0 {
		t.Fatalf("ordinary summary entries = %d, want 0", got)
	}
	if got := sctx.Tree.Len(); got != entriesBefore {
		t.Fatalf("entries = %d, want %d", got, entriesBefore)
	}
	msgs, _ := sctx.Tree.BuildContext()
	n := 0
	for _, m := range msgs {
		if m.Role == "compaction_summary" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("context summaries = %d, want 1", n)
	}
}
