package session

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// A session created with an ID chosen beforehand never replaces whatever
// is already at its path: a file (even damaged), a directory or a link.
func TestSaveNewNeverReplaces(t *testing.T) {
	for name, occupy := range map[string]func(t *testing.T, path string){
		"file": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{"id":"x`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"dangling link": func(t *testing.T, path string) {
			if err := os.Symlink(filepath.Join(filepath.Dir(path), "nowhere"), path); err != nil {
				t.Fatal(err)
			}
		},
		"directory": func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, err := NewFileStore(t.TempDir(), "/proj")
			if err != nil {
				t.Fatal(err)
			}
			id, err := NewID()
			if err != nil {
				t.Fatal(err)
			}
			path := store.path(id)
			occupy(t, path)
			before, _ := os.Lstat(path)
			sess := store.Create()
			sess.ID = id
			if err := store.SaveNew(sess); !errors.Is(err, fs.ErrExist) {
				t.Fatalf("SaveNew over an occupied path = %v, want fs.ErrExist", err)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() {
				t.Fatalf("occupant replaced: %v", err)
			}
			if ok, err := store.Exists(id); !ok || err != nil {
				t.Fatalf("Exists = %t, %v", ok, err)
			}
			tmps, _ := filepath.Glob(filepath.Join(store.Dir(), ".session-*.tmp"))
			if len(tmps) != 0 {
				t.Fatalf("temporary files left: %v", tmps)
			}
		})
	}
}

func TestSaveNewPublishesOnce(t *testing.T) {
	base := t.TempDir()
	store, err := NewFileStore(base, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := NewID()
	if ok, err := store.Exists(id); ok || err != nil {
		t.Fatalf("Exists before = %t, %v", ok, err)
	}
	sess := store.Create()
	sess.ID = id
	sess.Title = "first"
	if err := store.SaveNew(sess); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenFileStoreReadOnly(base, "/proj")
	if err != nil || ro.Dir() != store.Dir() {
		t.Fatalf("read-only store %v, %v", ro, err)
	}
	got, err := ro.LoadReadOnly(id)
	if err != nil || got.Title != "first" {
		t.Fatalf("load = %+v, %v", got, err)
	}
	// Later saves are ordinary upserts.
	sess.Title = "second"
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if ok, err := ExistsByID(base, id); !ok || err != nil {
		t.Fatalf("ExistsByID = %t, %v", ok, err)
	}
}

// The by-ID probe reads names only, includes the flat root, and reports an
// unreadable directory as an error, never as absence.
func TestExistsByID(t *testing.T) {
	base := t.TempDir()
	id, _ := NewID()
	if ok, err := ExistsByID(filepath.Join(base, "missing"), id); ok || err != nil {
		t.Fatalf("missing base = %t, %v", ok, err)
	}
	if err := os.WriteFile(filepath.Join(base, id+".json"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := ExistsByID(base, id); !ok || err != nil {
		t.Fatalf("damaged flat file = %t, %v", ok, err)
	}
	if err := os.Remove(filepath.Join(base, id+".json")); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads any directory")
	}
	if err := os.Chmod(base, 0o300); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(base, 0o700) }()
	if ok, err := ExistsByID(base, id); ok || err == nil {
		t.Fatalf("unreadable base = %t, %v; want an error", ok, err)
	}
}
