package session

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

func tempStore(t *testing.T) *FileStore {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "sessions")
	store, err := NewFileStore(dir, "")
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
	if err := store.Save(s1); err != nil {
		t.Fatalf("Save s1: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	s2 := store.Create()
	s2.Title = "second"
	if err := store.Save(s2); err != nil {
		t.Fatalf("Save s2: %v", err)
	}

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
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.Delete(sess.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Load should fail
	_, err := store.Load(sess.ID)
	if err == nil {
		t.Error("expected error loading deleted session")
	}

	// List should be empty
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
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
	entries, err := os.ReadDir(store.Dir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
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
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Delete(sess.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := store.Load(sess.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

// --- New: CWD scoping tests ---

func TestScopeKey_Uniqueness(t *testing.T) {
	// Paths that would collide with naive slash-to-dash replacement.
	k1 := scopeKey("/a/b")
	k2 := scopeKey("/a-b")
	if k1 == k2 {
		t.Errorf("scopeKey collision: %q and %q both produce %q", "/a/b", "/a-b", k1)
	}
}

func TestScopeKey_RootPath(t *testing.T) {
	key := scopeKey("/")
	if key == "" {
		t.Fatal("empty key for root path")
	}
	// Should not start with a separator.
	if key[0] == '/' {
		t.Errorf("key starts with separator: %q", key)
	}
	if len(key) < 5 {
		t.Errorf("key too short: %q", key)
	}
}

func TestScopeKey_Readability(t *testing.T) {
	key := scopeKey("/Users/foo/project")
	if len(key) < len("project_") {
		t.Fatalf("key too short: %q", key)
	}
	if key[:8] != "project_" {
		t.Errorf("key = %q, expected prefix 'project_'", key)
	}
}

func TestNewFileStore_ScopedDir(t *testing.T) {
	base := t.TempDir()
	store, err := NewFileStore(base, "/test/myproject")
	if err != nil {
		t.Fatal(err)
	}
	// Dir should be under base with the scope key.
	expected := filepath.Join(base, scopeKey("/test/myproject"))
	if store.Dir() != expected {
		t.Errorf("Dir() = %q, want %q", store.Dir(), expected)
	}
	// Directory should exist.
	if _, err := os.Stat(store.Dir()); err != nil {
		t.Errorf("scoped dir not created: %v", err)
	}
}

func TestSummary_IncludesMetadata(t *testing.T) {
	store := tempStore(t)

	sess := store.Create()
	sess.Title = "meta test"
	sess.Metadata["model"] = "claude-sonnet-4"
	sess.Metadata["cwd"] = "/test/project"
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	summaries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("got %d summaries, want 1", len(summaries))
	}
	if summaries[0].Metadata["model"] != "claude-sonnet-4" {
		t.Errorf("metadata[model] = %v, want 'claude-sonnet-4'", summaries[0].Metadata["model"])
	}
	if summaries[0].Metadata["cwd"] != "/test/project" {
		t.Errorf("metadata[cwd] = %v, want '/test/project'", summaries[0].Metadata["cwd"])
	}
}

func TestListAll(t *testing.T) {
	base := t.TempDir()

	// Create sessions in two different project stores.
	s1, err := NewFileStore(base, "/project/alpha")
	if err != nil {
		t.Fatal(err)
	}
	sess1 := s1.Create()
	sess1.Title = "alpha session"
	if err := s1.Save(sess1); err != nil {
		t.Fatalf("Save sess1: %v", err)
	}

	s2, err := NewFileStore(base, "/project/beta")
	if err != nil {
		t.Fatal(err)
	}
	sess2 := s2.Create()
	sess2.Title = "beta session"
	if err := s2.Save(sess2); err != nil {
		t.Fatalf("Save sess2: %v", err)
	}

	all, err := ListAll(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAll = %d, want 2", len(all))
	}

	titles := map[string]bool{all[0].Title: true, all[1].Title: true}
	if !titles["alpha session"] || !titles["beta session"] {
		t.Errorf("expected both sessions, got %v", titles)
	}
}

func TestFindSession(t *testing.T) {
	base := t.TempDir()

	store, err := NewFileStore(base, "/project/gamma")
	if err != nil {
		t.Fatal(err)
	}
	sess := store.Create()
	sess.Title = "findme"
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	found, foundStore, err := FindSession(base, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Title != "findme" {
		t.Errorf("found title = %q, want 'findme'", found.Title)
	}
	if foundStore.Dir() != store.Dir() {
		t.Errorf("found store dir = %q, want %q", foundStore.Dir(), store.Dir())
	}
}

func TestFindSession_NotFound(t *testing.T) {
	base := t.TempDir()
	_, _, err := FindSession(base, "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteByID(t *testing.T) {
	base := t.TempDir()

	store, err := NewFileStore(base, "/project/delta")
	if err != nil {
		t.Fatal(err)
	}
	sess := store.Create()
	sess.Title = "deleteme"
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := DeleteByID(base, sess.ID); err != nil {
		t.Fatal(err)
	}

	// Should be gone.
	_, err = store.Load(sess.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after DeleteByID, got %v", err)
	}
}

func TestDeleteByID_NotFound(t *testing.T) {
	base := t.TempDir()
	err := DeleteByID(base, "nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestFileStore_ConcurrentSave(t *testing.T) {
	base := t.TempDir()
	store, err := NewFileStore(base, "")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess := store.Create()
			sess.Title = "concurrent"
			if err := store.Save(sess); err != nil {
				t.Errorf("save failed: %v", err)
			}
		}()
	}
	wg.Wait()

	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 10 {
		t.Errorf("expected 10 sessions, got %d", len(list))
	}
}
