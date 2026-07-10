package bus

import (
	"sync"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
)

// TreeSyncer keeps a session.Tree in sync with agent message mutations.
// It subscribes to bus events and appends entries to the tree incrementally.
//
// Sync strategy:
//   - RunEnded: diff agent messages vs tree, append new entries
//   - CompactionEnded: append CompactionEntry, adjust sync point
//   - CommandExecuted("clear"): reset tree
//   - CommandExecuted (other): re-sync to catch AppendToConversation etc.
type TreeSyncer struct {
	tree *session.Tree
	sctx *SessionContext

	mu            sync.Mutex
	lastSyncCount int // number of agent messages at last sync
}

// RegisterTreeSyncer creates a TreeSyncer and subscribes to bus events.
// The tree must already be set on sctx.Tree.
func RegisterTreeSyncer(b EventBus, sctx *SessionContext) *TreeSyncer {
	ts := &TreeSyncer{
		tree: sctx.Tree,
		sctx: sctx,
	}

	// Set initial sync point: count agent messages already loaded
	ts.lastSyncCount = len(sctx.Agent.Messages())

	// Expose the syncer so GetDisplayMessages can include the in-flight turn.
	sctx.treeSyncer = ts

	// Tree mutations must share one ordered subscription. Typed subscriptions
	// have independent goroutines, so a RunEnded and CompactionEnded published
	// back-to-back could otherwise observe mutable agent state in either order.
	b.SubscribeAll(func(event any) {
		switch e := event.(type) {
		case RunEnded:
			ts.syncMessages()
			b.Publish(TreeSynced{SessionID: sctx.SessionID})
		case CompactionEnded:
			if e.Err != nil || e.Payload == nil {
				return
			}
			ts.handleCompaction(e)
			b.Publish(TreeSynced{SessionID: sctx.SessionID})
		case CommandExecuted:
			switch e.Command {
			case "clear":
				ts.mu.Lock()
				ts.tree.Clear()
				ts.lastSyncCount = 0
				ts.mu.Unlock()
			case "compact":
				// CompactionEnded records the compacted tree state.
			default:
				// Catch AppendToConversation and other direct mutations.
				ts.syncMessages()
			}
			b.Publish(TreeSynced{SessionID: sctx.SessionID})
		}
	})

	return ts
}

// DisplayMessages returns the full display history: the messages already synced
// to the tree PLUS any agent messages appended since the last sync (the
// in-flight turn). The tree only gains a turn's messages after RunEnded, so
// mid-run it lags by exactly the current turn. Without the tail, a WS reconnect
// during a run rebuilds from a snapshot missing the just-sent user message and
// the streaming reply, making them vanish until the run ends.
func (ts *TreeSyncer) DisplayMessages() []core.AgentMessage {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	treeMsgs := ts.tree.AllMessages()
	agentMsgs := ts.sctx.Agent.Messages()

	// Tail = agent messages not yet synced to the tree. Clamp defensively: a
	// branch/LoadState can shrink the agent below the last sync point before the
	// resync command runs, which would otherwise slice out of range.
	if ts.lastSyncCount >= len(agentMsgs) {
		return treeMsgs
	}
	tail := agentMsgs[ts.lastSyncCount:]
	out := make([]core.AgentMessage, 0, len(treeMsgs)+len(tail))
	out = append(out, treeMsgs...)
	out = append(out, tail...)
	return out
}

// syncMessages appends any new agent messages to the tree since the last sync.
func (ts *TreeSyncer) syncMessages() {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	msgs := ts.sctx.Agent.Messages()
	for i := ts.lastSyncCount; i < len(msgs); i++ {
		ts.tree.Append(session.Entry{
			Type:    session.EntryMessage,
			Message: msgs[i], // Tree.Append deep-copies
		})
	}
	ts.lastSyncCount = len(msgs)
}

// handleCompaction records a compaction in the tree.
// Pre-compaction messages are already in the tree from prior syncs.
// After compaction, agent state is: [compaction_summary, kept_msg_1, kept_msg_2, ...]
// We need to find which tree entry corresponds to kept_msg_1 (first non-summary).
func (ts *TreeSyncer) handleCompaction(e CompactionEnded) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	msgs := ts.sctx.Agent.Messages()

	// Find first kept entry ID in tree.
	// After compaction, msgs[0] is compaction_summary, msgs[1..] are kept messages.
	// The kept messages existed before compaction and were already synced to the tree.
	// Match by role + timestamp (unique enough for practical purposes).
	firstKeptID := ""
	if len(msgs) > 1 {
		firstKeptID = ts.findEntryByMessage(msgs[1])
	}

	ts.tree.Append(session.Entry{
		Type: session.EntryCompaction,
		Compaction: session.CompactionData{
			Summary:          e.Payload.Summary,
			FirstKeptEntryID: firstKeptID,
			TokensBefore:     e.Payload.TokensBefore,
			ReadFiles:        e.Payload.ReadFiles,
			ModifiedFiles:    e.Payload.ModifiedFiles,
		},
	})

	// After compaction the agent has fewer messages.
	ts.lastSyncCount = len(msgs)
}

// Reset re-points the syncer at a new tree and sync baseline. Used when the
// runtime loads a different session in place (TUI session switch), where the
// cached tree pointer and lastSyncCount would otherwise still reference the
// previous session.
func (ts *TreeSyncer) Reset(tree *session.Tree, syncCount int) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.tree = tree
	ts.lastSyncCount = syncCount
}

// findEntryByMessage finds the tree entry ID for a given agent message.
// Searches the current path backwards (most recent first) for a message
// matching by role + timestamp.
func (ts *TreeSyncer) findEntryByMessage(msg core.AgentMessage) string {
	path := ts.tree.Path()
	for i := len(path) - 1; i >= 0; i-- {
		e := path[i]
		if e.Type != session.EntryMessage {
			continue
		}
		if e.Message.Role == msg.Role && e.Message.Timestamp == msg.Timestamp {
			return e.ID
		}
	}
	return ""
}
