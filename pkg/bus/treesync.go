package bus

import (
	"fmt"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/session"
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

	mu     sync.Mutex
	synced map[string]struct{}
}

// RegisterTreeSyncer creates a TreeSyncer and subscribes to bus events.
// The tree must already be set on sctx.Tree.
func RegisterTreeSyncer(b EventBus, sctx *SessionContext) *TreeSyncer {
	ts := &TreeSyncer{
		tree: sctx.Tree,
		sctx: sctx,
	}

	ts.synced = make(map[string]struct{})
	for _, msg := range sctx.Tree.AllMessages() {
		if msg.MsgID != "" {
			ts.synced[msg.MsgID] = struct{}{}
		}
	}

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
				// ClearSession resets the agent and tree synchronously through
				// ResetAndClear below. Keeping this event case as a no-op preserves
				// the TreeSynced notification without leaving a window where a
				// reconnect can validate a token against the pre-clear tree.
			case "compact", "prepare-compact", "prepare-compact-noop":
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

// ResetAndClear resets the agent and clears its display tree as one history
// mutation. DisplayMessages and DisplayMessagesSince hold this same mutex while
// reading both stores, so a resume token can observe either the complete old
// history or the complete cleared history, never the reset agent with the old
// tree still accepting a stale token.
func (ts *TreeSyncer) ResetAndClear() error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if err := ts.sctx.Agent.Reset(); err != nil {
		return err
	}
	ts.tree.Clear()
	ts.synced = make(map[string]struct{})
	return nil
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

	return ts.appendInFlightTail(ts.tree.AllMessages())
}

// DisplayMessagesSince returns the durable tree suffix after entryID plus the
// current in-flight tail, and the timestamp of the anchor entry itself. It
// validates the token against the tree, rather than the lossy event stream, so
// it remains correct across reconnects and restarts.
//
// The anchor time is read from ts.tree under the SAME lock as the messages, on
// purpose: callers must not re-read sctx.Tree afterwards to date the anchor.
// That pointer is replaced on clear/resume, so an unlocked read races the
// replacement AND could date the anchor from a different tree than the one the
// messages came from.
func (ts *TreeSyncer) DisplayMessagesSince(entryID string) ([]core.AgentMessage, bool, time.Time) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	msgs, valid := ts.tree.DisplayMessagesSince(entryID)
	if !valid {
		return nil, false, time.Time{}
	}
	return ts.appendInFlightTail(msgs), true, anchorTimestamp(ts.tree, entryID)
}

// anchorTimestamp dates a validated resume token from the tree that produced
// it. It reports the anchor MESSAGE's creation time, not the entry's append
// time: entries reach the tree when a run ends, so the append time can be well
// after the client actually received the message, and anything that happened in
// that window would be wrongly treated as already known. Returns zero for a
// missing entry, a non-message entry or an undated message, which makes callers
// fail closed to sending everything rather than silently omitting state.
func anchorTimestamp(tree *session.Tree, entryID string) time.Time {
	if tree == nil || entryID == "" {
		return time.Time{}
	}
	entry, ok := tree.Entry(entryID)
	if !ok || entry.Type != session.EntryMessage || entry.Message.Timestamp == 0 {
		return time.Time{}
	}
	return time.Unix(entry.Message.Timestamp, 0)
}

// appendInFlightTail appends the agent messages not yet synced to the tree
// (the in-flight turn), skipping hidden internal prompts. Caller holds ts.mu.
func (ts *TreeSyncer) appendInFlightTail(msgs []core.AgentMessage) []core.AgentMessage {
	for i, msg := range ts.sctx.Agent.Messages() {
		if _, ok := ts.synced[messageSyncID(msg, i)]; ok || isHiddenInternalPrompt(msg) {
			continue
		}
		msgs = append(msgs, msg)
	}
	return msgs
}

// HasMsgID reports whether this ID belongs to a message that exists anywhere in
// the session tree (any branch, not just the current path) or in the in-flight
// turn not synced to the tree yet. Unlike DisplayMessages, which projects the
// current branch, this is the uniqueness domain for message identities: a
// message the current branch does not show is still reachable by branching back
// to it, so its ID is taken.
func (ts *TreeSyncer) HasMsgID(msgID string) bool {
	if msgID == "" {
		return false
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.tree.HasMsgID(msgID) {
		return true
	}
	for _, msg := range ts.sctx.Agent.Messages() {
		if msg.MsgID == msgID {
			return true
		}
	}
	return false
}

// syncMessages appends any new agent messages to the tree since the last sync.
func (ts *TreeSyncer) syncMessages() {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	msgs := ts.sctx.Agent.Messages()
	for i, msg := range msgs {
		id := messageSyncID(msg, i)
		if _, ok := ts.synced[id]; ok {
			continue
		}
		if isHiddenInternalPrompt(msg) {
			ts.synced[id] = struct{}{}
			continue
		}
		ts.tree.Append(session.Entry{
			Type:    session.EntryMessage,
			Message: msg,
		})
		ts.synced[id] = struct{}{}
	}
}

func isHiddenInternalPrompt(msg core.AgentMessage) bool {
	return msg.Role == "user" && msg.Custom != nil && msg.Custom["source"] == "prepare_compact"
}

func messageSyncID(msg core.AgentMessage, index int) string {
	if msg.MsgID != "" {
		return msg.MsgID
	}
	return fmt.Sprintf("legacy:%d", index)
}

// handleCompaction records a compaction in the tree.
// Pre-compaction messages are already in the tree from prior syncs.
// After compaction, agent state is: [compaction_summary, kept_msg_1, kept_msg_2, ...]
// We need to find which tree entry corresponds to kept_msg_1 (first non-summary).
func (ts *TreeSyncer) handleCompaction(e CompactionEnded) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	firstKeptID := e.Payload.FirstKeptMsgID
	marker := core.AgentMessage{}
	if e.Marker != nil {
		marker = session.DeepCopyMessage(*e.Marker)
	}

	ts.tree.Append(session.Entry{
		Type:    session.EntryCompaction,
		Message: marker,
		Compaction: session.CompactionData{
			Summary:          e.Payload.Summary,
			FirstKeptEntryID: firstKeptID,
			TokensBefore:     e.Payload.TokensBefore,
			ReadFiles:        e.Payload.ReadFiles,
			ModifiedFiles:    e.Payload.ModifiedFiles,
		},
	})

	// The summary is represented by the compaction entry, not an ordinary
	// message entry, but must not appear as an in-flight display tail.
	if e.Payload.SummaryMsgID != "" {
		ts.synced[e.Payload.SummaryMsgID] = struct{}{}
	}
}

// Reset re-points the syncer at a new tree and sync baseline. Used when the
// runtime loads a different session in place (TUI session switch), where the
// cached tree pointer and lastSyncCount would otherwise still reference the
// previous session.
func (ts *TreeSyncer) Reset(tree *session.Tree, syncCount int) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.tree = tree
	ts.synced = make(map[string]struct{})
	for _, msg := range tree.AllMessages() {
		if msg.MsgID != "" {
			ts.synced[msg.MsgID] = struct{}{}
		}
	}
}
