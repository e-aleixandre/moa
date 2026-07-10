package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	fakeTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return fakeTime }
	defer func() { nowFunc = time.Now }()

	s1 := store.Create()
	s1.Title = "first"
	if err := store.Save(s1); err != nil {
		t.Fatalf("Save s1: %v", err)
	}

	fakeTime = fakeTime.Add(time.Second)

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

	fakeTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return fakeTime }
	defer func() { nowFunc = time.Now }()

	s1 := store.Create()
	s1.Title = "first"
	if err := store.Save(s1); err != nil {
		t.Fatalf("Save s1: %v", err)
	}

	fakeTime = fakeTime.Add(time.Second)

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

// bigEntries builds a linear chain of n padded user-message entries, useful
// for exercising realistically-large session files in tests/benchmarks.
func bigEntries(n int) ([]Entry, string) {
	entries := make([]Entry, n)
	parent := ""
	for i := 0; i < n; i++ {
		id := generateEntryID()
		entries[i] = Entry{
			ID:       id,
			ParentID: parent,
			Type:     EntryMessage,
			Message: core.AgentMessage{
				Message: core.Message{
					Role:      "user",
					Content:   []core.Content{core.TextContent(strings.Repeat("x", 300))},
					Timestamp: time.Now().Unix(),
				},
			},
		}
		parent = id
	}
	return entries, parent
}

// TestList_LargeMetadataBeforeEntries verifies the streaming decoder finds
// the summary fields even when metadata is far larger than the old fixed
// 4KB read window, and that it still doesn't need a fallback full read to
// do so (it stops right at "entries").
func TestList_LargeMetadataBeforeEntries(t *testing.T) {
	store := tempStore(t)

	sess := store.Create()
	sess.Title = "big metadata"
	// >4KB of metadata — would have defeated the old fixed-prefix read.
	big := make([]string, 2000)
	for i := range big {
		big[i] = "some reasonably sized metadata value to pad things out"
	}
	sess.Metadata = map[string]any{"padding": big}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Sanity check the file really is bigger than the old 4KB limit.
	data, err := os.ReadFile(filepath.Join(store.dir, sess.ID+".json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) < 8192 {
		t.Fatalf("test fixture not big enough: %d bytes", len(data))
	}

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("List = %d, want 1", len(summaries))
	}
	if summaries[0].ID != sess.ID {
		t.Errorf("ID = %q, want %q", summaries[0].ID, sess.ID)
	}
	if summaries[0].Title != "big metadata" {
		t.Errorf("Title = %q, want %q", summaries[0].Title, "big metadata")
	}
	padding, ok := summaries[0].Metadata["padding"].([]any)
	if !ok || len(padding) != len(big) {
		t.Errorf("Metadata[padding] not decoded correctly: %v", summaries[0].Metadata["padding"])
	}
}

// TestList_LargeConversationSkipped verifies List doesn't need to decode
// large entries/messages arrays: it stops as soon as it sees the key.
func TestList_LargeConversationSkipped(t *testing.T) {
	store := tempStore(t)

	sess := store.Create()
	sess.Title = "big conversation"
	sess.Entries, sess.LeafID = bigEntries(5000)
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(store.dir, sess.ID+".json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) < 1024*1024 {
		t.Fatalf("test fixture not big enough: %d bytes", len(data))
	}

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 1 || summaries[0].ID != sess.ID {
		t.Fatalf("List = %+v, want single summary for %q", summaries, sess.ID)
	}
	if summaries[0].Title != "big conversation" {
		t.Errorf("Title = %q, want %q", summaries[0].Title, "big conversation")
	}
}

// TestDecodeSummaryPrefix_UnusualMetadataFallsBack verifies readSummary
// falls back to a full read when the streaming decode can't handle the
// document (here: top-level array instead of object).
func TestDecodeSummaryPrefix_UnusualMetadataFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "weird.json")
	if err := os.WriteFile(path, []byte(`["not", "an", "object"]`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := readSummary(path); err == nil {
		t.Error("expected error for non-object top level")
	}
}

// TestDecodeSummaryPrefix_TruncatedMidValueFallsBack simulates the exact
// failure mode the old fixed-prefix approach had: metadata truncated
// mid-value. The streaming decoder reads the whole (valid) document instead
// of a prefix, so this only exercises the readSummary fallback path when fed
// genuinely malformed JSON.
func TestDecodeSummaryPrefix_TruncatedMidValueFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "truncated.json")
	// Missing closing braces/quotes — genuinely malformed.
	if err := os.WriteFile(path, []byte(`{"id":"abc","title":"unterm`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := readSummary(path); err == nil {
		t.Error("expected error for truncated JSON")
	}
}

// BenchmarkList_ManySessions measures List() performance across many
// sessions with realistically sized conversation history, to justify the
// streaming-decoder approach over loading full files.
func BenchmarkList_ManySessions(b *testing.B) {
	dir := b.TempDir()
	store, err := NewFileStore(dir, "")
	if err != nil {
		b.Fatalf("NewFileStore: %v", err)
	}

	const numSessions = 50
	for i := 0; i < numSessions; i++ {
		sess := store.Create()
		sess.Title = "bench session"
		sess.Entries, sess.LeafID = bigEntries(500)
		if err := store.Save(sess); err != nil {
			b.Fatalf("Save: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.List(); err != nil {
			b.Fatalf("List: %v", err)
		}
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

func TestStoreRejectsTraversalIDs(t *testing.T) {
	store := tempStore(t)
	for _, id := range []string{"../victim", "..\\victim"} {
		if _, err := store.Load(id); err == nil {
			t.Errorf("Load(%q) succeeded", id)
		}
		if err := store.Delete(id); err == nil {
			t.Errorf("Delete(%q) succeeded", id)
		}
	}
}

func TestFindSessionMigratesV1(t *testing.T) {
	base := t.TempDir()
	store, err := NewFileStore(base, "/project")
	if err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef01234567"
	legacy := &Session{ID: id, Messages: []core.AgentMessage{core.WrapMessage(core.NewUserMessage("preserve me"))}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(), id+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	got, _, err := FindSession(base, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != SessionVersion || len(got.Entries) == 0 || len(got.Messages) != 0 {
		t.Fatalf("legacy session not migrated: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), id+".json.v1.bak")); err != nil {
		t.Fatalf("v1 backup missing: %v", err)
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

func TestSetArchived_PreservesUpdated(t *testing.T) {
	store := tempStore(t)

	fakeTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return fakeTime }
	defer func() { nowFunc = time.Now }()

	sess := store.Create()
	sess.Title = "archive me"
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	wantUpdated := sess.Updated

	// Advance the clock: if SetArchived bumped Updated we'd see it here.
	nowFunc = func() time.Time { return fakeTime.Add(time.Hour) }

	if err := store.SetArchived(sess.ID, true); err != nil {
		t.Fatalf("SetArchived(true): %v", err)
	}

	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.Archived {
		t.Error("expected Archived = true after SetArchived(true)")
	}
	if !loaded.Updated.Equal(wantUpdated) {
		t.Errorf("Updated = %v, want unchanged %v", loaded.Updated, wantUpdated)
	}

	// Unarchive: Updated must still be preserved.
	if err := store.SetArchived(sess.ID, false); err != nil {
		t.Fatalf("SetArchived(false): %v", err)
	}
	loaded, err = store.Load(sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Archived {
		t.Error("expected Archived = false after SetArchived(false)")
	}
	if !loaded.Updated.Equal(wantUpdated) {
		t.Errorf("Updated = %v, want unchanged %v", loaded.Updated, wantUpdated)
	}
}

func TestSetArchived_NotFound(t *testing.T) {
	store := tempStore(t)
	err := store.SetArchived("does-not-exist", true)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("SetArchived on missing session: got %v, want ErrNotFound", err)
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
