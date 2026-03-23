package session

import (
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestMigrateV1_BasicConversion(t *testing.T) {
	sess := &Session{
		ID:      "test",
		Version: 0,
		Created: time.Now(),
		Messages: []core.AgentMessage{
			{Message: core.NewUserMessage("hello")},
			{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("hi")}, Timestamp: time.Now().Unix()}},
		},
	}

	if err := MigrateV1ToV2(sess); err != nil {
		t.Fatalf("migration error: %v", err)
	}

	if sess.Version != SessionVersion {
		t.Fatalf("expected version %d, got %d", SessionVersion, sess.Version)
	}
	if len(sess.Messages) != 0 {
		t.Fatal("Messages should be cleared after migration")
	}
	if len(sess.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(sess.Entries))
	}
	// Verify parent chain
	if sess.Entries[0].ParentID != "" {
		t.Fatal("first entry should have empty parent")
	}
	if sess.Entries[1].ParentID != sess.Entries[0].ID {
		t.Fatal("second entry should point to first")
	}
	if sess.LeafID != sess.Entries[1].ID {
		t.Fatal("leaf should be last entry")
	}
	// Verify types
	if sess.Entries[0].Type != EntryMessage {
		t.Fatal("first entry should be message type")
	}
	if sess.Entries[0].Message.Role != "user" {
		t.Fatal("first entry should be user message")
	}
}

func TestMigrateV1_WithCompactionSummary(t *testing.T) {
	sess := &Session{
		ID:      "test",
		Version: 0,
		Created: time.Now(),
		Messages: []core.AgentMessage{
			{Message: core.Message{Role: "compaction_summary", Content: []core.Content{core.TextContent("summary of old stuff")}, Timestamp: time.Now().Unix()}},
			{Message: core.NewUserMessage("kept msg")},
			{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("kept reply")}, Timestamp: time.Now().Unix()}},
		},
	}

	if err := MigrateV1ToV2(sess); err != nil {
		t.Fatalf("migration error: %v", err)
	}

	if len(sess.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(sess.Entries))
	}
	// First entry should be a CompactionEntry, not a message
	if sess.Entries[0].Type != EntryCompaction {
		t.Fatalf("first entry should be compaction, got %s", sess.Entries[0].Type)
	}
	if sess.Entries[0].Compaction.Summary != "summary of old stuff" {
		t.Fatal("compaction summary wrong")
	}
	if sess.Entries[0].Compaction.FirstKeptEntryID != "migrated_1" {
		t.Fatalf("first kept should point to second entry, got %s", sess.Entries[0].Compaction.FirstKeptEntryID)
	}
	// Remaining entries should be messages
	if sess.Entries[1].Type != EntryMessage || sess.Entries[1].Message.Role != "user" {
		t.Fatal("second entry should be user message")
	}
}

func TestMigrateV1_EmptySession(t *testing.T) {
	sess := &Session{
		ID:      "test",
		Version: 0,
	}

	if err := MigrateV1ToV2(sess); err != nil {
		t.Fatalf("migration error: %v", err)
	}
	if sess.Version != SessionVersion {
		t.Fatal("version should be set")
	}
	if len(sess.Entries) != 0 {
		t.Fatal("should have no entries")
	}
}

func TestMigrateV1_AlreadyV2(t *testing.T) {
	sess := &Session{
		ID:      "test",
		Version: SessionVersion,
		Entries: []Entry{{ID: "a", Type: EntryMessage}},
	}

	if err := MigrateV1ToV2(sess); err != nil {
		t.Fatalf("should be no-op: %v", err)
	}
	// Should not touch existing entries
	if len(sess.Entries) != 1 {
		t.Fatal("should not modify v2 session")
	}
}

func TestMigrateV1_DeepCopy(t *testing.T) {
	original := core.AgentMessage{
		Message: core.NewUserMessage("hello"),
		Custom:  map[string]any{"key": "original"},
	}
	sess := &Session{
		ID:       "test",
		Version:  0,
		Created:  time.Now(),
		Messages: []core.AgentMessage{original},
	}

	if err := MigrateV1ToV2(sess); err != nil {
		t.Fatalf("migration error: %v", err)
	}

	// Mutate the original
	original.Custom["key"] = "mutated"

	// Entry should be unaffected
	if sess.Entries[0].Message.Custom["key"] != "original" {
		t.Fatal("migration should deep copy messages")
	}
}

func TestMigrateV1_TimestampHandling(t *testing.T) {
	now := time.Now()
	sess := &Session{
		ID:      "test",
		Version: 0,
		Created: now,
		Messages: []core.AgentMessage{
			{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("no timestamp")}}},
		},
	}

	if err := MigrateV1ToV2(sess); err != nil {
		t.Fatalf("migration error: %v", err)
	}

	// With zero timestamp, should derive from session Created
	ts := sess.Entries[0].Timestamp
	if ts.Before(now) || ts.After(now.Add(time.Second)) {
		t.Fatalf("entry timestamp should be near session created time, got %v", ts)
	}
}
