package session

import (
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// --- helpers ---

func userEntry(text string) Entry {
	return Entry{
		Type:    EntryMessage,
		Message: core.AgentMessage{Message: core.NewUserMessage(text)},
	}
}

func assistantEntry(text string) Entry {
	return Entry{
		Type: EntryMessage,
		Message: core.AgentMessage{
			Message: core.Message{
				Role:      "assistant",
				Content:   []core.Content{core.TextContent(text)},
				Timestamp: time.Now().Unix(),
			},
		},
	}
}

func toolResultEntry(callID, toolName, result string) Entry {
	return Entry{
		Type: EntryMessage,
		Message: core.AgentMessage{
			Message: core.NewToolResultMessage(callID, toolName, []core.Content{core.TextContent(result)}, false),
		},
	}
}

func compactionEntry(summary, firstKeptID string, tokensBefore int) Entry {
	return Entry{
		Type: EntryCompaction,
		Compaction: CompactionData{
			Summary:          summary,
			FirstKeptEntryID: firstKeptID,
			TokensBefore:     tokensBefore,
		},
	}
}

func configEntry(model string) Entry {
	return Entry{
		Type:   EntryConfig,
		Config: ConfigChangeData{Model: model},
	}
}

func labelEntry(label string) Entry {
	return Entry{
		Type:  EntryLabel,
		Label: label,
	}
}

// --- Append tests ---

func TestAppend_LinearChain(t *testing.T) {
	tree := NewTree()

	id1 := tree.Append(userEntry("hello"))
	id2 := tree.Append(assistantEntry("hi there"))
	id3 := tree.Append(userEntry("how are you"))

	if tree.LeafID() != id3 {
		t.Fatalf("leaf should be %s, got %s", id3, tree.LeafID())
	}

	path := tree.Path()
	if len(path) != 3 {
		t.Fatalf("expected 3 entries in path, got %d", len(path))
	}
	if path[0].ID != id1 || path[1].ID != id2 || path[2].ID != id3 {
		t.Fatal("path order is wrong")
	}
	// Verify parent chain
	if path[0].ParentID != "" {
		t.Fatal("root entry should have empty parent")
	}
	if path[1].ParentID != id1 {
		t.Fatalf("second entry parent should be %s, got %s", id1, path[1].ParentID)
	}
	if path[2].ParentID != id2 {
		t.Fatalf("third entry parent should be %s, got %s", id2, path[2].ParentID)
	}
}

func TestAppend_SetsTimestamp(t *testing.T) {
	tree := NewTree()
	before := time.Now()
	tree.Append(userEntry("test"))
	after := time.Now()

	entry := tree.Path()[0]
	if entry.Timestamp.Before(before) || entry.Timestamp.After(after) {
		t.Fatal("timestamp should be between before and after")
	}
}

func TestAppend_DeepCopiesMessage(t *testing.T) {
	tree := NewTree()

	msg := core.AgentMessage{
		Message: core.NewUserMessage("original"),
		Custom:  map[string]any{"key": "original"},
	}
	tree.Append(Entry{Type: EntryMessage, Message: msg})

	// Mutate the original
	msg.Custom["key"] = "mutated"
	msg.Content[0].Text = "mutated"

	// Tree entry should be unaffected
	stored := tree.Path()[0]
	if stored.Message.Custom["key"] != "original" {
		t.Fatal("deep copy failed: Custom map was mutated")
	}
	if stored.Message.Content[0].Text != "original" {
		t.Fatal("deep copy failed: Content was mutated")
	}
}

func TestAppend_EmptyTree(t *testing.T) {
	tree := NewTree()
	if tree.LeafID() != "" {
		t.Fatal("empty tree should have empty leaf")
	}
	if path := tree.Path(); path != nil {
		t.Fatal("empty tree should have nil path")
	}
}

// --- Branch tests ---

func TestBranch_CreatesNewPath(t *testing.T) {
	tree := NewTree()

	id1 := tree.Append(userEntry("hello"))
	id2 := tree.Append(assistantEntry("hi"))
	_ = tree.Append(userEntry("path A"))

	// Branch back to id2
	if err := tree.Branch(id2); err != nil {
		t.Fatalf("branch error: %v", err)
	}
	if tree.LeafID() != id2 {
		t.Fatal("leaf should be at branch point")
	}

	// Append on new branch
	idB := tree.Append(userEntry("path B"))

	// New path should be: id1 → id2 → idB
	path := tree.Path()
	if len(path) != 3 {
		t.Fatalf("expected 3 entries on new branch, got %d", len(path))
	}
	if path[0].ID != id1 || path[1].ID != id2 || path[2].ID != idB {
		t.Fatal("new branch path is wrong")
	}

	// Original entries still exist (total 4 entries)
	if tree.Len() != 4 {
		t.Fatalf("expected 4 total entries, got %d", tree.Len())
	}
}

func TestBranch_RejectsToolResult(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("hello"))
	tree.Append(assistantEntry("let me check..."))
	trID := tree.Append(toolResultEntry("tc1", "bash", "done"))

	err := tree.Branch(trID)
	if err == nil {
		t.Fatal("should reject branching to tool_result")
	}
	if !strings.Contains(err.Error(), "tool_result") {
		t.Fatalf("error should mention tool_result, got: %v", err)
	}
}

func TestBranch_RejectsInvalidID(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("hello"))

	err := tree.Branch("nonexistent")
	if err == nil {
		t.Fatal("should reject unknown entry ID")
	}
}

func TestBranch_AllowsCompactionEntry(t *testing.T) {
	tree := NewTree()
	id1 := tree.Append(userEntry("hello"))
	tree.Append(assistantEntry("hi"))
	compID := tree.Append(compactionEntry("summary", id1, 5000))

	if err := tree.Branch(compID); err != nil {
		t.Fatalf("should allow branching to compaction entry: %v", err)
	}
}

func TestBranch_AllowsUserEntry(t *testing.T) {
	tree := NewTree()
	id1 := tree.Append(userEntry("hello"))
	tree.Append(assistantEntry("hi"))

	if err := tree.Branch(id1); err != nil {
		t.Fatalf("should allow branching to user entry: %v", err)
	}
}

// --- Children tests ---

func TestChildren_ReturnsDirectChildren(t *testing.T) {
	tree := NewTree()

	id1 := tree.Append(userEntry("root"))
	tree.Append(userEntry("child A"))

	// Branch back and create child B
	_ = tree.Branch(id1)
	tree.Append(userEntry("child B"))

	children := tree.Children(id1)
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
}

func TestChildren_EmptyForLeaf(t *testing.T) {
	tree := NewTree()
	id1 := tree.Append(userEntry("only"))

	children := tree.Children(id1)
	if len(children) != 0 {
		t.Fatalf("expected 0 children for leaf, got %d", len(children))
	}
}

// --- BuildContext tests ---

func TestBuildContext_NoCompaction(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("hello"))
	tree.Append(assistantEntry("hi"))
	tree.Append(userEntry("how are you"))

	msgs, epoch := tree.BuildContext()
	if epoch != 0 {
		t.Fatalf("expected epoch 0, got %d", epoch)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" || msgs[2].Role != "user" {
		t.Fatal("wrong message roles")
	}
}

func TestBuildContext_SingleCompaction(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("old msg 1"))
	tree.Append(assistantEntry("old reply 1"))
	id3 := tree.Append(userEntry("kept msg"))
	tree.Append(assistantEntry("kept reply"))

	// Add compaction pointing to id3 as first kept
	tree.Append(compactionEntry("summary of old stuff", id3, 10000))

	tree.Append(userEntry("new msg"))

	msgs, epoch := tree.BuildContext()
	if epoch != 1 {
		t.Fatalf("expected epoch 1, got %d", epoch)
	}
	// Should be: compaction_summary, kept_msg, kept_reply, new_msg
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "compaction_summary" {
		t.Fatalf("first message should be compaction_summary, got %s", msgs[0].Role)
	}
	if msgs[0].Content[0].Text != "summary of old stuff" {
		t.Fatal("wrong summary text")
	}
	if msgs[1].Role != "user" {
		t.Fatalf("second message should be user (kept), got %s", msgs[1].Role)
	}
}

func TestBuildContext_MultipleCompactions(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("very old"))
	tree.Append(assistantEntry("very old reply"))
	id3 := tree.Append(userEntry("old"))
	tree.Append(assistantEntry("old reply"))

	// First compaction
	tree.Append(compactionEntry("first summary", id3, 5000))

	id6 := tree.Append(userEntry("recent"))
	tree.Append(assistantEntry("recent reply"))

	// Second compaction
	tree.Append(compactionEntry("second summary", id6, 8000))

	tree.Append(userEntry("newest"))

	msgs, epoch := tree.BuildContext()
	if epoch != 2 {
		t.Fatalf("expected epoch 2, got %d", epoch)
	}
	// Should use second compaction: summary + recent, recent_reply, newest
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "compaction_summary" {
		t.Fatal("first should be compaction_summary")
	}
	if msgs[0].Content[0].Text != "second summary" {
		t.Fatalf("should use second compaction summary, got: %s", msgs[0].Content[0].Text)
	}
}

func TestBuildContext_FiltersNonMessages(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("hello"))
	tree.Append(configEntry("claude-4"))   // should be filtered
	tree.Append(labelEntry("bookmark"))    // should be filtered
	tree.Append(assistantEntry("hi"))

	msgs, epoch := tree.BuildContext()
	if epoch != 0 {
		t.Fatal("should have no compactions")
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (config/label filtered), got %d", len(msgs))
	}
}

func TestBuildContext_FiltersNonLLMRoles(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("hello"))

	// Non-LLM role message (session_event)
	tree.Append(Entry{
		Type: EntryMessage,
		Message: core.AgentMessage{
			Message: core.Message{
				Role:    "session_event",
				Content: []core.Content{core.TextContent("something happened")},
			},
		},
	})

	tree.Append(assistantEntry("hi"))

	msgs, _ := tree.BuildContext()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (session_event filtered), got %d", len(msgs))
	}
}

func TestBuildContext_EmptyTree(t *testing.T) {
	tree := NewTree()
	msgs, epoch := tree.BuildContext()
	if msgs != nil {
		t.Fatal("empty tree should return nil messages")
	}
	if epoch != 0 {
		t.Fatal("empty tree should return epoch 0")
	}
}

// --- AllMessages tests ---

func TestAllMessages_IncludesEverything(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("old msg"))
	tree.Append(assistantEntry("old reply"))
	id3 := tree.Append(userEntry("kept msg"))

	// Compaction
	tree.Append(compactionEntry("summary", id3, 5000))

	tree.Append(userEntry("new msg"))

	msgs := tree.AllMessages()
	// Should include ALL messages: old_msg, old_reply, kept_msg, compaction_marker, new_msg
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages in AllMessages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content[0].Text != "old msg" {
		t.Fatal("first should be old user message")
	}
	if msgs[3].Role != "session_event" {
		t.Fatalf("compaction should become session_event, got %s", msgs[3].Role)
	}
}

func TestAllMessages_CompactionMarker(t *testing.T) {
	tree := NewTree()
	id1 := tree.Append(userEntry("hello"))
	tree.Append(compactionEntry("test summary", id1, 3000))

	msgs := tree.AllMessages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2, got %d", len(msgs))
	}
	marker := msgs[1]
	if marker.Role != "session_event" {
		t.Fatalf("expected session_event, got %s", marker.Role)
	}
	if marker.Custom["type"] != "compaction_marker" {
		t.Fatal("should have compaction_marker custom type")
	}
	if !strings.Contains(marker.Content[0].Text, "3K") {
		t.Fatalf("should contain token count, got: %s", marker.Content[0].Text)
	}
}

func TestAllMessages_IncludesNonLLMRoles(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("hello"))
	tree.Append(Entry{
		Type: EntryMessage,
		Message: core.AgentMessage{
			Message: core.Message{
				Role:    "session_event",
				Content: []core.Content{core.TextContent("event")},
			},
		},
	})

	msgs := tree.AllMessages()
	if len(msgs) != 2 {
		t.Fatalf("AllMessages should include all roles, got %d", len(msgs))
	}
}

func TestAllMessages_EmptyTree(t *testing.T) {
	tree := NewTree()
	msgs := tree.AllMessages()
	if msgs != nil {
		t.Fatal("empty tree should return nil")
	}
}

// --- NewTreeFromEntries tests ---

func TestNewTreeFromEntries_Valid(t *testing.T) {
	entries := []Entry{
		{ID: "a", ParentID: "", Type: EntryMessage, Message: core.AgentMessage{Message: core.NewUserMessage("hello")}},
		{ID: "b", ParentID: "a", Type: EntryMessage, Message: core.AgentMessage{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("hi")}}}},
	}

	tree, err := NewTreeFromEntries(entries, "b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tree.LeafID() != "b" {
		t.Fatal("wrong leaf")
	}
	path := tree.Path()
	if len(path) != 2 {
		t.Fatalf("expected 2 entries in path, got %d", len(path))
	}
}

func TestNewTreeFromEntries_DuplicateID(t *testing.T) {
	entries := []Entry{
		{ID: "a", ParentID: "", Type: EntryMessage},
		{ID: "a", ParentID: "", Type: EntryMessage},
	}

	_, err := NewTreeFromEntries(entries, "a")
	if err == nil {
		t.Fatal("should detect duplicate ID")
	}
}

func TestNewTreeFromEntries_OrphanParent(t *testing.T) {
	entries := []Entry{
		{ID: "a", ParentID: "missing", Type: EntryMessage},
	}

	_, err := NewTreeFromEntries(entries, "a")
	if err == nil {
		t.Fatal("should detect missing parent")
	}
}

func TestNewTreeFromEntries_LeafNotFound(t *testing.T) {
	entries := []Entry{
		{ID: "a", ParentID: "", Type: EntryMessage},
	}

	_, err := NewTreeFromEntries(entries, "missing")
	if err == nil {
		t.Fatal("should detect missing leaf")
	}
}

func TestNewTreeFromEntries_EmptyLeaf(t *testing.T) {
	// Empty leaf is valid (empty tree or all entries present but no active branch)
	entries := []Entry{
		{ID: "a", ParentID: "", Type: EntryMessage},
	}

	tree, err := NewTreeFromEntries(entries, "")
	if err != nil {
		t.Fatalf("empty leaf should be valid: %v", err)
	}
	if tree.Path() != nil {
		t.Fatal("empty leaf should give nil path")
	}
}

func TestNewTreeFromEntries_Cycle(t *testing.T) {
	// Create entries that form a cycle: a→b→a
	entries := []Entry{
		{ID: "a", ParentID: "b", Type: EntryMessage},
		{ID: "b", ParentID: "a", Type: EntryMessage},
	}

	_, err := NewTreeFromEntries(entries, "a")
	if err == nil {
		t.Fatal("should detect cycle")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error should mention cycle, got: %v", err)
	}
}

// --- Clear test ---

func TestClear(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("hello"))
	tree.Append(assistantEntry("hi"))

	tree.Clear()

	if tree.LeafID() != "" {
		t.Fatal("cleared tree should have empty leaf")
	}
	if tree.Len() != 0 {
		t.Fatal("cleared tree should have 0 entries")
	}
	if tree.Path() != nil {
		t.Fatal("cleared tree should have nil path")
	}
}

// --- Entry lookup test ---

func TestEntry_Lookup(t *testing.T) {
	tree := NewTree()
	id := tree.Append(userEntry("hello"))

	e, ok := tree.Entry(id)
	if !ok {
		t.Fatal("should find entry")
	}
	if e.Message.Content[0].Text != "hello" {
		t.Fatal("wrong entry content")
	}

	_, ok = tree.Entry("nonexistent")
	if ok {
		t.Fatal("should not find nonexistent entry")
	}
}

// --- BuildContext with branching ---

func TestBuildContext_AfterBranch(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("hello"))
	id2 := tree.Append(assistantEntry("hi"))
	tree.Append(userEntry("path A"))

	// Branch and go a different direction
	_ = tree.Branch(id2)
	tree.Append(userEntry("path B"))

	msgs, _ := tree.BuildContext()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages on branch B, got %d", len(msgs))
	}
	if msgs[2].Content[0].Text != "path B" {
		t.Fatalf("last message should be 'path B', got %s", msgs[2].Content[0].Text)
	}
}

func TestBuildContext_BranchAfterCompaction(t *testing.T) {
	tree := NewTree()
	tree.Append(userEntry("old"))
	tree.Append(assistantEntry("old reply"))
	id3 := tree.Append(userEntry("kept"))
	tree.Append(assistantEntry("kept reply"))
	compID := tree.Append(compactionEntry("summary", id3, 5000))
	tree.Append(userEntry("path A"))

	// Branch back to compaction entry
	_ = tree.Branch(compID)
	tree.Append(userEntry("path B"))

	msgs, epoch := tree.BuildContext()
	if epoch != 1 {
		t.Fatalf("expected epoch 1, got %d", epoch)
	}
	// summary + kept + kept_reply + path_B
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "compaction_summary" {
		t.Fatal("first should be compaction_summary")
	}
	if msgs[3].Content[0].Text != "path B" {
		t.Fatal("last should be path B")
	}
}
