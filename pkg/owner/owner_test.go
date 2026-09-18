package owner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestCreateWritesOwnerAndSeedsBook(t *testing.T) {
	cfg := t.TempDir()
	root := t.TempDir()
	store := NewStore(cfg)

	own, err := store.Create(root, "Winerim", "opus", "low", true)
	if err != nil {
		t.Fatal(err)
	}
	if own.ID == "" || !strings.HasPrefix(own.ID, "own_") {
		t.Fatalf("owner id = %q, want an own_ prefixed id", own.ID)
	}
	if own.CodebaseKey != core.CodebaseKey(root) {
		t.Fatalf("codebase key = %q, want %q", own.CodebaseKey, core.CodebaseKey(root))
	}
	data, err := os.ReadFile(filepath.Join(cfg, "codebases", own.CodebaseKey, "owner.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk Owner
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.Name != "Winerim" || !onDisk.AnswerAsks {
		t.Fatalf("persisted owner = %+v", onDisk)
	}
	if _, err := os.Stat(filepath.Join(store.BookDir(own.CodebaseKey), ProjectFile)); err != nil {
		t.Fatalf("book not seeded: %v", err)
	}
}

func TestCreateRefusesSecondOwnerForSameCodebase(t *testing.T) {
	store := NewStore(t.TempDir())
	root := t.TempDir()
	if _, err := store.Create(root, "First", "", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(root, "Second", "", "", true); err != ErrExists {
		t.Fatalf("second create error = %v, want ErrExists", err)
	}
}

func TestCreateKeepsAnExistingBook(t *testing.T) {
	cfg := t.TempDir()
	root := t.TempDir()
	store := NewStore(cfg)
	key := core.CodebaseKey(root)
	if err := os.MkdirAll(store.BookDir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(store.BookDir(key), ProjectFile)
	if err := os.WriteFile(existing, []byte("# kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(root, "Winerim", "", "", true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# kept" {
		t.Fatalf("book overwritten: %q", data)
	}
}

func TestFindByDirAndByID(t *testing.T) {
	store := NewStore(t.TempDir())
	root := t.TempDir()
	own, err := store.Create(root, "Winerim", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	found, ok, err := store.FindByDir(root)
	if err != nil || !ok || found.ID != own.ID {
		t.Fatalf("FindByDir = %+v, %v, %v", found, ok, err)
	}
	byID, ok, err := store.FindByID(own.ID)
	if err != nil || !ok || byID.Name != "Winerim" {
		t.Fatalf("FindByID = %+v, %v, %v", byID, ok, err)
	}
	if _, ok, err := store.FindByID("own_missing"); err != nil || ok {
		t.Fatalf("FindByID(missing) = %v, %v", ok, err)
	}
}

func TestFindByCodebaseReportsCorruptFile(t *testing.T) {
	cfg := t.TempDir()
	store := NewStore(cfg)
	key := "deadbeef"
	dir := filepath.Join(cfg, "codebases", key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "owner.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.FindByCodebase(key); err == nil || found {
		t.Fatalf("corrupt owner.json read as found=%v err=%v; must be an error", found, err)
	}
}

func TestDeleteKeepsTheBook(t *testing.T) {
	store := NewStore(t.TempDir())
	root := t.TempDir()
	own, err := store.Create(root, "Winerim", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(own.CodebaseKey); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.FindByCodebase(own.CodebaseKey); err != nil || found {
		t.Fatalf("owner still found after delete: %v %v", found, err)
	}
	if _, err := os.Stat(filepath.Join(store.BookDir(own.CodebaseKey), ProjectFile)); err != nil {
		t.Fatalf("book removed with the owner: %v", err)
	}
	if err := store.Delete(own.CodebaseKey); err != ErrNotFound {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}

func TestListSortsByName(t *testing.T) {
	cfg := t.TempDir()
	store := NewStore(cfg)
	for _, name := range []string{"Zeta", "Alpha"} {
		if _, err := store.Create(t.TempDir(), name, "", "", true); err != nil {
			t.Fatal(err)
		}
	}
	owners, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 2 || owners[0].Name != "Alpha" || owners[1].Name != "Zeta" {
		t.Fatalf("List = %+v", owners)
	}
}

func TestProjectIndexIsBounded(t *testing.T) {
	store := NewStore(t.TempDir())
	root := t.TempDir()
	own, err := store.Create(root, "Winerim", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("á", MaxProjectBytes) // 2 bytes per rune: twice the cap
	if err := os.WriteFile(filepath.Join(store.BookDir(own.CodebaseKey), ProjectFile), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	index := store.ProjectIndex(own.CodebaseKey)
	if len(index) > MaxProjectBytes {
		t.Fatalf("project index = %d bytes, want <= %d", len(index), MaxProjectBytes)
	}
	if !utf8Valid(index) {
		t.Fatal("project index was cut mid-rune")
	}
	if index == "" {
		t.Fatal("project index empty for a non-empty PROJECT.md")
	}
}

func TestProjectIndexEmptyWithoutBook(t *testing.T) {
	store := NewStore(t.TempDir())
	if got := store.ProjectIndex("nokey"); got != "" {
		t.Fatalf("ProjectIndex = %q, want empty", got)
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

// Two concurrent creations for the same codebase: exactly one wins, and the
// loser sees ErrExists rather than overwriting the owner that already exists.
func TestConcurrentCreateYieldsOneOwner(t *testing.T) {
	cfg := t.TempDir()
	root := t.TempDir()
	store := NewStore(cfg)

	start := make(chan struct{})
	type result struct {
		own Owner
		err error
	}
	results := make(chan result, 2)
	for _, name := range []string{"First", "Second"} {
		go func() {
			<-start
			own, err := store.Create(root, name, "opus", "low", true)
			results <- result{own, err}
		}()
	}
	close(start)

	var created, exists int
	for range 2 {
		res := <-results
		switch res.err {
		case nil:
			created++
		case ErrExists:
			exists++
		default:
			t.Fatalf("unexpected Create error: %v", res.err)
		}
	}
	if created != 1 || exists != 1 {
		t.Fatalf("created = %d, ErrExists = %d, want 1 and 1", created, exists)
	}
	owners, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 {
		t.Fatalf("owners on disk = %d, want 1", len(owners))
	}
}
