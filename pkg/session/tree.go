package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// Tree is an append-only entry log that supports branching.
// It maintains an index for O(1) lookup and a leaf pointer for the current branch tip.
//
// Key methods:
//   - BuildContext() returns messages for the LLM (handles compaction)
//   - AllMessages() returns ALL messages along the current path (for display)
//   - Branch() moves the leaf to enable forking
type Tree struct {
	entries []Entry        // append-only log (all entries from all branches)
	index   map[string]int // entry ID → index in entries slice
	leafID  string         // current branch tip
}

// NewTree creates an empty tree.
func NewTree() *Tree {
	return &Tree{
		index: make(map[string]int),
	}
}

// NewTreeFromEntries reconstructs a tree from persisted entries.
// Returns an error if the entries are invalid (see ValidateEntries).
func NewTreeFromEntries(entries []Entry, leafID string) (*Tree, error) {
	t := &Tree{
		entries: make([]Entry, len(entries)),
		index:   make(map[string]int, len(entries)),
		leafID:  leafID,
	}
	copy(t.entries, entries)
	for i, e := range t.entries {
		if _, exists := t.index[e.ID]; exists {
			return nil, fmt.Errorf("tree: duplicate entry ID: %s", e.ID)
		}
		t.index[e.ID] = i
	}
	// Validate parent references
	for _, e := range t.entries {
		if e.ParentID != "" {
			if _, ok := t.index[e.ParentID]; !ok {
				return nil, fmt.Errorf("tree: entry %s references missing parent %s", e.ID, e.ParentID)
			}
		}
	}
	if leafID != "" {
		if _, ok := t.index[leafID]; !ok {
			return nil, fmt.Errorf("tree: leaf %s not found in entries", leafID)
		}
		// Cycle detection: walk from leaf to root
		if err := t.detectCycle(leafID); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// detectCycle walks from the given entry to root, detecting cycles.
func (t *Tree) detectCycle(startID string) error {
	visited := make(map[string]bool)
	id := startID
	for id != "" {
		if visited[id] {
			return fmt.Errorf("tree: cycle detected at entry %s", id)
		}
		visited[id] = true
		idx, ok := t.index[id]
		if !ok {
			return fmt.Errorf("tree: missing entry %s in cycle check", id)
		}
		id = t.entries[idx].ParentID
	}
	return nil
}

// Append adds an entry as a child of the current leaf and advances the leaf.
// The entry's ID and Timestamp are set automatically. ParentID is set to the current leaf.
// The Message field is deep-copied to enforce immutability.
// Returns the assigned entry ID.
func (t *Tree) Append(e Entry) string {
	e.ID = generateEntryID()
	e.ParentID = t.leafID
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	// Deep copy the message to prevent external mutation
	if e.Type == EntryMessage {
		e.Message = DeepCopyMessage(e.Message)
	}
	t.entries = append(t.entries, e)
	t.index[e.ID] = len(t.entries) - 1
	t.leafID = e.ID
	return e.ID
}

// Branch moves the leaf pointer to the given entry ID.
// Next Append creates a child of that entry (starting a new branch).
// Returns an error if:
//   - the entry ID doesn't exist
//   - the target is a tool_result (would leave dangling tool_call)
func (t *Tree) Branch(entryID string) error {
	idx, ok := t.index[entryID]
	if !ok {
		return fmt.Errorf("tree: unknown entry ID: %s", entryID)
	}
	e := t.entries[idx]
	if e.Type == EntryMessage && e.Message.Role == "tool_result" {
		return fmt.Errorf("tree: cannot branch to tool_result entry (would leave dangling tool_call)")
	}
	t.leafID = entryID
	return nil
}

// Clear resets the tree to empty state.
func (t *Tree) Clear() {
	t.entries = nil
	t.index = make(map[string]int)
	t.leafID = ""
}

// LeafID returns the current branch tip entry ID.
func (t *Tree) LeafID() string {
	return t.leafID
}

// Entries returns a copy of all entries (for persistence).
func (t *Tree) Entries() []Entry {
	cp := make([]Entry, len(t.entries))
	copy(cp, t.entries)
	return cp
}

// Len returns the total number of entries across all branches.
func (t *Tree) Len() int {
	return len(t.entries)
}

// Entry returns an entry by ID.
func (t *Tree) Entry(id string) (Entry, bool) {
	idx, ok := t.index[id]
	if !ok {
		return Entry{}, false
	}
	return t.entries[idx], true
}

// Path returns entries from root to the current leaf, in order.
func (t *Tree) Path() []Entry {
	if t.leafID == "" {
		return nil
	}
	return t.pathTo(t.leafID)
}

// pathTo returns entries from root to the given entry ID, in order.
func (t *Tree) pathTo(id string) []Entry {
	var stack []Entry
	for id != "" {
		idx, ok := t.index[id]
		if !ok {
			break
		}
		stack = append(stack, t.entries[idx])
		id = t.entries[idx].ParentID
	}
	// Reverse: stack is leaf→root, we want root→leaf
	for i, j := 0, len(stack)-1; i < j; i, j = i+1, j-1 {
		stack[i], stack[j] = stack[j], stack[i]
	}
	return stack
}

// Children returns direct children of the given entry.
func (t *Tree) Children(entryID string) []Entry {
	var children []Entry
	for _, e := range t.entries {
		if e.ParentID == entryID {
			children = append(children, e)
		}
	}
	return children
}

// BuildContext returns messages for the LLM provider and the compaction epoch.
//
// Algorithm:
//  1. Walk the current path (root → leaf)
//  2. Find the LAST compaction entry (most recent wins for multi-compaction)
//  3. If compaction found: emit compaction_summary + messages from firstKeptEntryID → leaf
//  4. If no compaction: emit all message entries
//  5. Only include LLM-relevant roles (user, assistant, tool_result, compaction_summary)
func (t *Tree) BuildContext() ([]core.AgentMessage, int) {
	path := t.Path()
	if len(path) == 0 {
		return nil, 0
	}

	// Find the last compaction entry and count total compactions (= epoch)
	var lastCompaction *Entry
	epoch := 0
	for i := range path {
		if path[i].Type == EntryCompaction {
			lastCompaction = &path[i]
			epoch++
		}
	}

	if lastCompaction == nil {
		// No compaction: emit all message entries
		return t.collectMessages(path), 0
	}

	// With compaction: summary + messages from firstKeptEntryID onward
	var msgs []core.AgentMessage

	// Emit compaction summary as first message
	msgs = append(msgs, core.AgentMessage{
		Message: core.Message{
			Role:    "compaction_summary",
			Content: []core.Content{core.TextContent(lastCompaction.Compaction.Summary)},
		},
	})

	// Find firstKeptEntryID in the path and emit from there
	collecting := false
	for _, e := range path {
		if e.ID == lastCompaction.Compaction.FirstKeptEntryID {
			collecting = true
		}
		if collecting && e.Type == EntryMessage && isLLMRole(e.Message.Role) {
			msgs = append(msgs, e.Message)
		}
	}

	return msgs, epoch
}

// AllMessages returns ALL messages along the current path (for display).
// Includes pre-compaction messages. Compaction entries become synthetic status messages.
func (t *Tree) AllMessages() []core.AgentMessage {
	path := t.Path()
	if len(path) == 0 {
		return nil
	}

	var msgs []core.AgentMessage
	for _, e := range path {
		switch e.Type {
		case EntryMessage:
			msgs = append(msgs, e.Message)
		case EntryCompaction:
			// Synthetic status message for display
			text := fmt.Sprintf("✂ Context compacted (%dK tokens summarized)", e.Compaction.TokensBefore/1000)
			msgs = append(msgs, core.AgentMessage{
				Message: core.Message{
					Role:      "session_event",
					Content:   []core.Content{core.TextContent(text)},
					Timestamp: e.Timestamp.Unix(),
				},
				Custom: map[string]any{"type": "compaction_marker"},
			})
		}
	}
	return msgs
}

// isLLMRole returns true if the role should be included in LLM context.
func isLLMRole(role string) bool {
	switch role {
	case "user", "assistant", "tool_result", "compaction_summary":
		return true
	default:
		return false
	}
}

// collectMessages extracts AgentMessages from message entries on the path.
func (t *Tree) collectMessages(path []Entry) []core.AgentMessage {
	var msgs []core.AgentMessage
	for _, e := range path {
		if e.Type == EntryMessage && isLLMRole(e.Message.Role) {
			msgs = append(msgs, e.Message)
		}
	}
	return msgs
}

// generateEntryID creates a unique entry ID (16 hex chars from 8 random bytes).
func generateEntryID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
