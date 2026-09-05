package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestArtifactStore(t *testing.T) (*ArtifactStore, string) {
	t.Helper()
	dir := t.TempDir()
	return NewArtifactStore(dir, "aaaaaaaaaaaaaaaaaaaaaaaa"), dir
}

func TestArtifactStore_UpsertPersistsAndDedupes(t *testing.T) {
	store, _ := newTestArtifactStore(t)

	first, err := store.Upsert("/data/report.md", ArtifactMeta{Name: "report.md", Title: "Report", Mime: "text/markdown", Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || len(first.ID) != 32 {
		t.Fatalf("artifact ID = %q, want 32 hex chars", first.ID)
	}

	second, err := store.Upsert("/data/report.md", ArtifactMeta{Name: "report-v2.md", Mime: "text/markdown", Size: 42})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Errorf("re-send changed the ID: %q -> %q", first.ID, second.ID)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("re-send changed CreatedAt: %v -> %v", first.CreatedAt, second.CreatedAt)
	}
	if second.Name != "report-v2.md" || second.Size != 42 {
		t.Errorf("re-send did not refresh observed metadata: %#v", second)
	}
	if second.Title != "Report" {
		t.Errorf("re-send erased the title: %#v", second)
	}

	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("catalog has %d entries, want 1", len(list))
	}

	// A different path in the same conversation is a separate reference.
	other, err := store.Upsert("/data/other.md", ArtifactMeta{Name: "other.md", Mime: "text/markdown", Size: 1})
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == first.ID {
		t.Fatal("distinct paths share an artifact ID")
	}
	if list, _ = store.List(); len(list) != 2 {
		t.Fatalf("catalog has %d entries, want 2", len(list))
	}
}

func TestArtifactStore_ReinstantiationSeesSameCatalog(t *testing.T) {
	store, dir := newTestArtifactStore(t)
	published, err := store.Upsert("/data/report.md", ArtifactMeta{Name: "report.md", Mime: "text/markdown", Size: 3})
	if err != nil {
		t.Fatal(err)
	}

	// A fresh instance over the same directory — the restart case.
	reopened := NewArtifactStore(dir, "aaaaaaaaaaaaaaaaaaaaaaaa")
	got, ok, err := reopened.Get(published.ID)
	if err != nil || !ok {
		t.Fatalf("Get after reinstantiation = %v, %v", ok, err)
	}
	if got.Path != "/data/report.md" || got.ID != published.ID {
		t.Errorf("reopened artifact = %#v", got)
	}
}

func TestArtifactStore_ListOrderedByUpdatedDescending(t *testing.T) {
	store, _ := newTestArtifactStore(t)
	a, err := store.Upsert("/data/a", ArtifactMeta{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	b, err := store.Upsert("/data/b", ArtifactMeta{Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := store.Upsert("/data/a", ArtifactMeta{Name: "a"}); err != nil {
		t.Fatal(err)
	}

	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("order = %v, want the re-sent artifact first", []string{list[0].ID, list[1].ID})
	}
}

func TestArtifactStore_DeleteReferencesTombstonesWrites(t *testing.T) {
	store, dir := newTestArtifactStore(t)
	if _, err := store.Upsert("/data/report.md", ArtifactMeta{Name: "report.md"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteReferences(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "aaaaaaaaaaaaaaaaaaaaaaaa.artifacts")); !os.IsNotExist(err) {
		t.Fatalf("sidecar still present after DeleteReferences: %v", err)
	}
	if _, err := store.Upsert("/data/late.md", ArtifactMeta{Name: "late.md"}); err == nil {
		t.Fatal("a late publication recreated the catalog of a deleted session")
	}
	if _, err := os.Stat(filepath.Join(dir, "aaaaaaaaaaaaaaaaaaaaaaaa.artifacts")); !os.IsNotExist(err) {
		t.Fatal("late publication recreated the sidecar")
	}
	// Deleting twice is fine.
	if err := store.DeleteReferences(); err != nil {
		t.Fatalf("second DeleteReferences: %v", err)
	}
}

// TestArtifactStore_ConcurrentDeleteAndUpsert runs the real race the lifecycle
// has: a publication in flight while the conversation is deleted. Either the
// upsert wins and is then removed, or it fails — never a recreated sidecar.
func TestArtifactStore_ConcurrentDeleteAndUpsert(t *testing.T) {
	store, dir := newTestArtifactStore(t)
	sidecar := filepath.Join(dir, "aaaaaaaaaaaaaaaaaaaaaaaa.artifacts")

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _ = store.Upsert("/data/file", ArtifactMeta{Name: "file"})
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		_ = store.DeleteReferences()
	}()
	close(start)
	wg.Wait()

	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatalf("sidecar survived a concurrent delete: %v", err)
	}
	if _, err := store.Upsert("/data/file", ArtifactMeta{Name: "file"}); err == nil {
		t.Fatal("upsert allowed after delete")
	}
}

func TestArtifactStore_ConcurrentUpsertsKeepOneEntryPerPath(t *testing.T) {
	store, _ := newTestArtifactStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Upsert("/data/file", ArtifactMeta{Name: "file"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("concurrent re-sends produced %d entries, want 1", len(list))
	}
}

// TestArtifactStore_SidecarIgnoredByFileStoreList is why the sidecar is not
// named "<id>.artifacts.json": FileStore.List reads every "*.json" header, and
// a catalog there would be opened on every session listing.
func TestArtifactStore_SidecarIgnoredByFileStoreList(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStore(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	sess := fs.Create()
	if err := fs.Save(sess); err != nil {
		t.Fatal(err)
	}
	store := NewArtifactStore(fs.Dir(), sess.ID)
	if _, err := store.Upsert("/data/report.md", ArtifactMeta{Name: "report.md"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(store.Path(), ".artifacts") || strings.HasSuffix(store.Path(), ".json") {
		t.Fatalf("sidecar path = %q, want a non-.json extension", store.Path())
	}

	summaries, err := fs.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].ID != sess.ID {
		t.Fatalf("List() = %#v, want only the session itself", summaries)
	}
}

func TestArtifactStore_CorruptCatalogIsAnError(t *testing.T) {
	store, dir := newTestArtifactStore(t)
	if err := os.WriteFile(filepath.Join(dir, "aaaaaaaaaaaaaaaaaaaaaaaa.artifacts"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("corrupt catalog listed without error")
	}
	if _, _, err := store.Get("x"); err == nil {
		t.Fatal("corrupt catalog read without error")
	}
}

func TestArtifactStore_SidecarShapeAndPermissions(t *testing.T) {
	store, dir := newTestArtifactStore(t)
	if _, err := store.Upsert("/data/report.md", ArtifactMeta{Name: "report.md", Mime: "text/markdown", Size: 7}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "aaaaaaaaaaaaaaaaaaaaaaaa.artifacts")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("sidecar mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cat struct {
		Version   int `json:"version"`
		Artifacts []struct {
			ID   string `json:"id"`
			Path string `json:"path"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(data, &cat); err != nil {
		t.Fatalf("sidecar is not valid JSON: %v", err)
	}
	if cat.Version != ArtifactsVersion || len(cat.Artifacts) != 1 || cat.Artifacts[0].Path != "/data/report.md" {
		t.Fatalf("sidecar content = %s", data)
	}
	// No temporary leftovers after a successful write.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("directory holds %d files, want just the sidecar", len(entries))
	}
}

func TestRemoveArtifacts(t *testing.T) {
	store, dir := newTestArtifactStore(t)
	if _, err := store.Upsert("/data/a", ArtifactMeta{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveArtifacts(dir, "aaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Path()); !os.IsNotExist(err) {
		t.Fatalf("sidecar survived RemoveArtifacts: %v", err)
	}
	// Idempotent: nothing to remove is not an error.
	if err := RemoveArtifacts(dir, "aaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatalf("RemoveArtifacts on a missing sidecar: %v", err)
	}
}
