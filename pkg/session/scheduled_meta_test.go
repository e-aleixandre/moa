package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The scheduled-destination markers and the creator's timezone are written at
// creation and must survive every runtime snapshot.
func TestScheduledMetadataIsPreserved(t *testing.T) {
	meta := map[string]any{
		MetaScheduledOccurrenceID: "12",
		MetaScheduledTaskID:       "3",
		MetaCreatorTZ:             "Europe/Madrid",
		MetaModel:                 "anthropic/x",
	}
	got := PreservedMetadata(meta)
	for _, k := range []string{MetaScheduledOccurrenceID, MetaScheduledTaskID, MetaCreatorTZ} {
		if got[k] != meta[k] {
			t.Fatalf("%s not preserved: %+v", k, got)
		}
	}
	if _, ok := got[MetaModel]; ok {
		t.Fatal("runtime key preserved")
	}
}

func TestFindByMetadata(t *testing.T) {
	base := t.TempDir()
	mk := func(cwd string, meta map[string]any) *Session {
		store, err := NewFileStore(base, cwd)
		if err != nil {
			t.Fatal(err)
		}
		s := store.Create()
		s.Metadata = meta
		if err := store.Save(s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	a := mk("/w/a", map[string]any{MetaScheduledOccurrenceID: "7"})
	mk("/w/b", map[string]any{MetaScheduledOccurrenceID: "8"})
	mk("/w/b", nil)
	got, err := FindByMetadata(base, MetaScheduledOccurrenceID, "7")
	if err != nil || len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("find = %+v, %v", got, err)
	}

	// An unreadable file that mentions the key makes the answer incomplete.
	dir := filepath.Dir(filepath.Join(base, "x"))
	entries, _ := os.ReadDir(dir)
	var storeDir string
	for _, e := range entries {
		if e.IsDir() {
			storeDir = filepath.Join(dir, e.Name())
			break
		}
	}
	if err := os.WriteFile(filepath.Join(storeDir, "broken.json"), []byte(`{"id":"zz","metadata":{"`+MetaScheduledOccurrenceID+`":"7"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FindByMetadata(base, MetaScheduledOccurrenceID, "7"); err == nil || !strings.Contains(err.Error(), "broken.json") {
		t.Fatalf("unreadable candidate ignored: %v", err)
	}
	// An unrelated unreadable file does not block the search.
	if err := os.WriteFile(filepath.Join(storeDir, "broken.json"), []byte(`{"id":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := FindByMetadata(base, MetaScheduledOccurrenceID, "7"); err != nil || len(got) != 1 {
		t.Fatalf("unrelated broken file: %+v %v", got, err)
	}
}
