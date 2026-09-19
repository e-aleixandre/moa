package book

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// OWNER.md is the user's half of the contract. The refusal has to survive the
// spellings of the same path: checking the literal before normalizing it is
// how "./OWNER.md" quietly becomes writable.
func TestReservedFileRefusedWhateverTheSpelling(t *testing.T) {
	dir := t.TempDir()
	tool := NewTool(dir, true)
	for _, spelling := range []string{"OWNER.md", "./OWNER.md", "areas/../OWNER.md", "./areas/erp/../../OWNER.md", "/OWNER.md"} {
		res := run(t, tool, map[string]any{"action": "write", "path": spelling, "content": "merge whenever"})
		if !res.IsError {
			t.Fatalf("%q was accepted as a write target", spelling)
		}
		if _, err := os.Stat(filepath.Join(dir, OwnerFile)); err == nil {
			t.Fatalf("%q created OWNER.md", spelling)
		}
	}
}

// A child of the owner never sees the user's preferences file: "never to
// children" is invisibility (list, read, search), not a refusal to write.
func TestChildVariantCannotSeeTheOwnerFile(t *testing.T) {
	dir := t.TempDir()
	writeSheet(t, dir, OwnerFile, "# Preferences\n\nEscalate anything touching albaranes to me.\n")
	writeSheet(t, dir, "areas/erp/albaranes.md", "---\ntitle: Albaranes\n---\n\nImportación de albaranes.\n")

	child, ok := ReadOnlyVariant(NewTool(dir, true))
	if !ok {
		t.Fatal("the book tool was not downgraded")
	}
	listing := resultText(run(t, child, map[string]any{"action": "list"}))
	if strings.Contains(listing, OwnerFile) {
		t.Fatalf("a child listed the user's preferences: %q", listing)
	}
	for _, spelling := range []string{"OWNER.md", "./OWNER.md", "areas/../OWNER.md"} {
		res := run(t, child, map[string]any{"action": "read", "path": spelling})
		if !res.IsError || strings.Contains(resultText(res), "Escalate") {
			t.Fatalf("a child read the user's preferences through %q: %q", spelling, resultText(res))
		}
	}
	found := resultText(run(t, child, map[string]any{"action": "search", "query": "escalate preferences"}))
	if strings.Contains(found, OwnerFile) {
		t.Fatalf("a child found the user's preferences: %q", found)
	}
	if res := run(t, child, map[string]any{"action": "write", "path": "areas/erp/albaranes.md", "content": "x"}); !res.IsError {
		t.Fatal("a child wrote the book")
	}
	// What it is for still works.
	if got := resultText(run(t, child, map[string]any{"action": "read", "path": "areas/erp/albaranes.md"})); !strings.Contains(got, "Importación") {
		t.Fatalf("a child cannot read the book: %q", got)
	}

	// The owner itself still reads OWNER.md: it is its instructions.
	owned := resultText(run(t, NewTool(dir, true), map[string]any{"action": "read", "path": OwnerFile}))
	if !strings.Contains(owned, "Escalate") {
		t.Fatalf("the owner cannot read its own preferences: %q", owned)
	}
}

// A symlink inside the book pointing out of it is not a door: the book sits in
// the config directory, next to memory and credentials.
func TestSymlinkOutOfTheBookIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secret, []byte("api key: 1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "leak.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	writeSheet(t, dir, "areas/a/sheet.md", "---\ntitle: Sheet\n---\n\nalbaranes\n")

	tool := NewTool(dir, true)
	for _, path := range []string{"leak.md", "elsewhere/secret.md", "areas/../elsewhere/secret.md"} {
		res := run(t, tool, map[string]any{"action": "read", "path": path})
		if !res.IsError || strings.Contains(resultText(res), "api key") {
			t.Fatalf("read through %q leaked outside the book: %q", path, resultText(res))
		}
	}
	// The walk does not follow them either: a listing is what the model sees.
	listing := resultText(run(t, tool, map[string]any{"action": "list"}))
	if strings.Contains(listing, "leak.md") || strings.Contains(listing, "secret.md") {
		t.Fatalf("the walk followed a symlink out of the book: %q", listing)
	}
	if hits, _, err := SearchBook(dir, "api key", 10); err != nil || len(hits) != 0 {
		t.Fatalf("search followed a symlink out of the book: %+v (%v)", hits, err)
	}
	if res := run(t, tool, map[string]any{"action": "write", "path": "elsewhere/new.md", "content": "x"}); !res.IsError {
		t.Fatal("a write escaped the book through a symlinked directory")
	}
}

// touch backdates a book file by hours, so a listing's relative age is a fact
// of the test rather than of the clock.
func touch(t *testing.T, dir, rel string, hours int) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	when := time.Now().Add(time.Duration(hours) * time.Hour)
	if err := os.Chtimes(full, when, when); err != nil {
		t.Fatal(err)
	}
}
