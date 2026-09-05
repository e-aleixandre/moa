package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestFindSessionStoreReadOnly_FindsOwningStore(t *testing.T) {
	base := t.TempDir()
	other, err := NewFileStore(base, "/tmp/other-project")
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Save(other.Create()); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(base, "/tmp/target-project")
	if err != nil {
		t.Fatal(err)
	}
	sess := store.Create()
	sess.Entries = nil
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}

	found, err := FindSessionStoreReadOnly(base, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Dir() != store.Dir() {
		t.Errorf("store dir = %q, want %q", found.Dir(), store.Dir())
	}
}

// TestFindSessionStoreReadOnly_DoesNotLoadEntries is the reason this helper
// exists: FindSessionReadOnly decodes the whole transcript on every call, and
// artifact requests only need the directory.
func TestFindSessionStoreReadOnly_DoesNotLoadEntries(t *testing.T) {
	base := t.TempDir()
	store, err := NewFileStore(base, "/tmp/heavy")
	if err != nil {
		t.Fatal(err)
	}
	sess := store.Create()
	sess.Messages = []core.AgentMessage{{Message: core.Message{Role: "user", Content: []core.Content{{Type: "text", Text: strings.Repeat("x", 200_000)}}}}}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	// Truncating the tail leaves the header intact but the history unreadable:
	// a locator that parsed entries would fail here.
	path := filepath.Join(store.Dir(), sess.ID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data[:len(data)/2], 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := FindSessionStoreReadOnly(base, sess.ID); err != nil {
		t.Fatalf("locator needed the transcript: %v", err)
	}
	if _, _, err := FindSessionReadOnly(base, sess.ID); err == nil {
		t.Fatal("expected the full read-only load to fail on the truncated file (test premise)")
	}
}

func TestFindSessionStoreReadOnly_MissingIsNotFound(t *testing.T) {
	base := t.TempDir()
	if _, err := NewFileStore(base, "/tmp/project"); err != nil {
		t.Fatal(err)
	}
	_, err := FindSessionStoreReadOnly(base, "aaaaaaaaaaaaaaaaaaaaaaaa")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := FindSessionStoreReadOnly(base, "not-a-valid-id"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid ID err = %v, want ErrNotFound", err)
	}
}

// A candidate that exists but whose header is unreadable or names another
// session is a real error, so a handler can answer 500 instead of pretending
// the conversation never existed.
func TestFindSessionStoreReadOnly_CorruptHeaderIsError(t *testing.T) {
	base := t.TempDir()
	store, err := NewFileStore(base, "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	id := "bbbbbbbbbbbbbbbbbbbbbbbb"
	if err := os.WriteFile(filepath.Join(store.Dir(), id+".json"), []byte("{oops"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := FindSessionStoreReadOnly(base, id); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("corrupt header err = %v, want a non-ErrNotFound error", err)
	}

	mismatch := "cccccccccccccccccccccccc"
	if err := os.WriteFile(filepath.Join(store.Dir(), mismatch+".json"), []byte(`{"id":"dddddddddddddddddddddddd","version":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := FindSessionStoreReadOnly(base, mismatch); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("header ID mismatch err = %v, want a non-ErrNotFound error", err)
	}
}
