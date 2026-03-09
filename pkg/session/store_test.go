package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

func tempStore(t *testing.T) *FileStore {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sessions")
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return store
}

func TestCreateAndSave(t *testing.T) {
	store := tempStore(t)

	sess := store.Create()
	if sess.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if sess.Created.IsZero() {
		t.Fatal("expected non-zero Created")
	}

	sess.Title = "test session"
	sess.Messages = []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("hello")),
	}

	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file exists
	path := filepath.Join(store.Dir(), sess.ID+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("session file not found: %v", err)
	}
}

func TestSaveAndLoad(t *testing.T) {
	store := tempStore(t)

	sess := store.Create()
	sess.Title = "roundtrip test"
	sess.Messages = []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("hello")),
		{Message: core.Message{
			Role:    "assistant",
			Content: []core.Content{{Type: "text", Text: "world"}},
		}},
	}
	sess.Metadata["model"] = "claude-sonnet-4"

	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.ID != sess.ID {
		t.Errorf("ID = %q, want %q", loaded.ID, sess.ID)
	}
	if loaded.Title != "roundtrip test" {
		t.Errorf("Title = %q, want 'roundtrip test'", loaded.Title)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("Messages = %d, want 2", len(loaded.Messages))
	}
	if loaded.Messages[0].Role != "user" {
		t.Errorf("Messages[0].Role = %q, want 'user'", loaded.Messages[0].Role)
	}
	if loaded.Messages[1].Content[0].Text != "world" {
		t.Errorf("Messages[1] text = %q, want 'world'", loaded.Messages[1].Content[0].Text)
	}
	if loaded.Metadata["model"] != "claude-sonnet-4" {
		t.Errorf("Metadata[model] = %v, want 'claude-sonnet-4'", loaded.Metadata["model"])
	}
}

func TestLatest(t *testing.T) {
	store := tempStore(t)

	// Create two sessions with different update times
	s1 := store.Create()
	s1.Title = "first"
	if err := store.Save(s1); err != nil {
		t.Fatalf("Save s1: %v", err)
	}

	// Ensure different timestamp
	time.Sleep(10 * time.Millisecond)

	s2 := store.Create()
	s2.Title = "second"
	if err := store.Save(s2); err != nil {
		t.Fatalf("Save s2: %v", err)
	}

	latest, err := store.Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest == nil {
		t.Fatal("expected non-nil latest")
	}
	if latest.ID != s2.ID {
		t.Errorf("Latest ID = %q, want %q (s2)", latest.ID, s2.ID)
	}
}

func TestLatest_Empty(t *testing.T) {
	store := tempStore(t)

	latest, err := store.Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest != nil {
		t.Error("expected nil latest for empty store")
	}
}

func TestList(t *testing.T) {
	store := tempStore(t)

	s1 := store.Create()
	s1.Title = "first"
	store.Save(s1)

	time.Sleep(10 * time.Millisecond)

	s2 := store.Create()
	s2.Title = "second"
	store.Save(s2)

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("List = %d, want 2", len(summaries))
	}
	// Sorted by Updated desc — s2 first
	if summaries[0].ID != s2.ID {
		t.Errorf("summaries[0].ID = %q, want %q (s2)", summaries[0].ID, s2.ID)
	}
	if summaries[1].ID != s1.ID {
		t.Errorf("summaries[1].ID = %q, want %q (s1)", summaries[1].ID, s1.ID)
	}
}

func TestDelete(t *testing.T) {
	store := tempStore(t)

	sess := store.Create()
	sess.Title = "to delete"
	store.Save(sess)

	if err := store.Delete(sess.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Load should fail
	_, err := store.Load(sess.ID)
	if err == nil {
		t.Error("expected error loading deleted session")
	}

	// List should be empty
	summaries, _ := store.List()
	if len(summaries) != 0 {
		t.Errorf("List = %d, want 0 after delete", len(summaries))
	}
}

func TestDelete_NonExistent(t *testing.T) {
	store := tempStore(t)

	// Should not error on non-existent session
	if err := store.Delete("nonexistent"); err != nil {
		t.Errorf("Delete non-existent: %v", err)
	}
}

func TestAtomicWrite_NoCorrruption(t *testing.T) {
	store := tempStore(t)

	sess := store.Create()
	sess.Title = "atomic test"
	sess.Messages = []core.AgentMessage{
		core.WrapMessage(core.NewUserMessage("hello")),
	}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// No .tmp files should remain
	entries, _ := os.ReadDir(store.Dir())
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("found leftover tmp file: %s", e.Name())
		}
	}
}

func TestSetTitle(t *testing.T) {
	sess := &Session{}

	// First call sets title
	sess.SetTitle("Hello, how are you doing today?", 20)
	if sess.Title != "Hello, how are you d…" {
		t.Errorf("Title = %q, want truncated", sess.Title)
	}

	// Second call is a no-op (title already set)
	sess.SetTitle("Different title", 20)
	if sess.Title != "Hello, how are you d…" {
		t.Error("SetTitle should not overwrite existing title")
	}
}

func TestSetTitle_Empty(t *testing.T) {
	sess := &Session{}

	sess.SetTitle("", 20)
	if sess.Title != "" {
		t.Error("SetTitle should not set empty string")
	}
}

func TestSetTitle_Short(t *testing.T) {
	sess := &Session{}

	sess.SetTitle("Hi", 20)
	if sess.Title != "Hi" {
		t.Errorf("Title = %q, want 'Hi'", sess.Title)
	}
}

func TestFileStore_Load_NotFound(t *testing.T) {
	store := tempStore(t)
	_, err := store.Load("nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestFileStore_Delete_ThenLoad_NotFound(t *testing.T) {
	store := tempStore(t)
	sess := store.Create()
	store.Save(sess)
	store.Delete(sess.ID)

	_, err := store.Load(sess.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}
