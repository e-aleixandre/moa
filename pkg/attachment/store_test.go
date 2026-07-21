package attachment

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const (
	sessionOne   = "0123456789abcdef01234567"
	sessionTwo   = "89abcdef0123456701234567"
	sessionThree = "fedcba987654321001234567"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func put(t *testing.T, s *Store, data []byte) Descriptor {
	t.Helper()
	d, err := s.Put(data, PutMeta{Name: "../example.png", Mime: "image/png", Kind: "image", Width: 3, Height: 2})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func readCatalog(t *testing.T, s *Store) catalog {
	t.Helper()
	data, err := os.ReadFile(s.catalogPath())
	if err != nil {
		t.Fatal(err)
	}
	var got catalog
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPutDeduplicatesAndMintsOccurrences(t *testing.T) {
	s := newTestStore(t)
	data := []byte("same image bytes")
	first := put(t, s, data)
	second := put(t, s, data)

	if first.SHA256 != second.SHA256 {
		t.Fatalf("hashes differ: %q != %q", first.SHA256, second.SHA256)
	}
	if first.ID == second.ID {
		t.Fatalf("Put reused occurrence ID %q", first.ID)
	}
	if first.Name != "example.png" {
		t.Fatalf("name was not sanitized: %q", first.Name)
	}
	entries, err := os.ReadDir(filepath.Dir(s.blobPath(first.SHA256)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != first.SHA256 {
		t.Fatalf("expected one deduplicated blob, got %#v", entries)
	}
}

func TestAddRefCountsOccurrencesAndIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	first := put(t, s, []byte("same"))
	second := put(t, s, []byte("same"))

	for _, d := range []Descriptor{first, first, second} {
		if err := s.AddRef(sessionOne, d); err != nil {
			t.Fatal(err)
		}
	}
	got := readCatalog(t, s)[first.SHA256]
	if got.RefCount != 2 {
		t.Fatalf("refcount = %d, want 2", got.RefCount)
	}
}

func TestPutRefStoresAndReferencesImmediately(t *testing.T) {
	s := newTestStore(t)
	d, err := s.PutRef(sessionOne, []byte("owned immediately"), PutMeta{Name: "example.txt", Kind: "file"})
	if err != nil {
		t.Fatal(err)
	}
	f, got, err := s.Open(sessionOne, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("owned immediately")) || !sameDescriptor(got, d) {
		t.Fatalf("Open returned data=%q descriptor=%+v", data, got)
	}
	if got := readCatalog(t, s)[d.SHA256].RefCount; got != 1 {
		t.Fatalf("refcount = %d, want 1", got)
	}
}

func TestRemoveRefRemovesOccurrenceAndGarbageCollects(t *testing.T) {
	s := newTestStore(t)
	d, err := s.PutRef(sessionOne, []byte("remove me"), PutMeta{Name: "image.png", Mime: "image/png", Kind: "image"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveRef(sessionOne, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup(sessionOne, d.ID); ok {
		t.Fatal("removed occurrence is still indexed")
	}
	if _, err := os.Stat(s.blobPath(d.SHA256)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("blob still exists or unexpected stat error: %v", err)
	}
	if err := s.RemoveRef(sessionOne, d.ID); err != nil {
		t.Fatalf("idempotent removal failed: %v", err)
	}
}

func TestReconcileExistingReleasesDeadIndexes(t *testing.T) {
	s := newTestStore(t)
	shared := []byte("shared")
	liveOnly, err := s.PutRef(sessionOne, []byte("live"), PutMeta{Name: "live.png", Mime: "image/png", Kind: "image"})
	if err != nil {
		t.Fatal(err)
	}
	deadOnly, err := s.PutRef(sessionTwo, []byte("dead"), PutMeta{Name: "dead.png", Mime: "image/png", Kind: "image"})
	if err != nil {
		t.Fatal(err)
	}
	sharedLive, err := s.PutRef(sessionOne, shared, PutMeta{Name: "shared.png", Mime: "image/png", Kind: "image"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutRef(sessionTwo, shared, PutMeta{Name: "shared.png", Mime: "image/png", Kind: "image"}); err != nil {
		t.Fatal(err)
	}

	if err := s.ReconcileExisting(map[string]bool{sessionOne: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup(sessionOne, liveOnly.ID); !ok {
		t.Fatal("live index was removed")
	}
	if _, ok := s.Lookup(sessionOne, sharedLive.ID); !ok {
		t.Fatal("live shared reference was removed")
	}
	if _, err := os.Stat(s.blobPath(deadOnly.SHA256)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dead-only blob still exists or unexpected stat error: %v", err)
	}
	if _, err := os.Stat(s.blobPath(sharedLive.SHA256)); err != nil {
		t.Fatalf("shared blob was removed: %v", err)
	}
}

func TestAddRefRebuildsMissingOrCorruptCatalog(t *testing.T) {
	s := newTestStore(t)
	first := put(t, s, []byte("same"))
	second := put(t, s, []byte("same"))
	third := put(t, s, []byte("same"))
	if err := s.AddRef(sessionOne, first); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(s.catalogPath(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRef(sessionOne, second); err != nil {
		t.Fatal(err)
	}
	if got := readCatalog(t, s)[first.SHA256].RefCount; got != 2 {
		t.Fatalf("refcount after corrupt catalog = %d, want 2", got)
	}

	if err := os.Remove(s.catalogPath()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRef(sessionOne, third); err != nil {
		t.Fatal(err)
	}
	if got := readCatalog(t, s)[first.SHA256].RefCount; got != 3 {
		t.Fatalf("refcount after missing catalog = %d, want 3", got)
	}
}

func TestOpenOnlyAllowsOwningSession(t *testing.T) {
	s := newTestStore(t)
	d := put(t, s, []byte("owned bytes"))
	if err := s.AddRef(sessionOne, d); err != nil {
		t.Fatal(err)
	}

	f, got, err := s.Open(sessionOne, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("owned bytes")) || !sameDescriptor(got, d) {
		t.Fatalf("Open returned data=%q descriptor=%+v", data, got)
	}

	for _, tc := range []struct {
		name      string
		sessionID string
		attID     string
	}{
		{name: "wrong session", sessionID: sessionTwo, attID: d.ID},
		{name: "unknown attachment", sessionID: sessionOne, attID: "att_aaaaaaaaaaaaaaaaaaaaaaaa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := s.Open(tc.sessionID, tc.attID)
			if !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("Open error = %v, want fs.ErrNotExist", err)
			}
		})
	}
}

func TestOpenRejectsUnsafeBlobTypes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace func(t *testing.T, path string)
	}{
		{
			name: "symlink",
			replace: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
		},
		{
			name: "directory",
			replace: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			d := put(t, s, []byte("unsafe test"))
			if err := s.AddRef(sessionOne, d); err != nil {
				t.Fatal(err)
			}
			tc.replace(t, s.blobPath(d.SHA256))
			if _, _, err := s.Open(sessionOne, d.ID); err == nil {
				t.Fatal("Open accepted unsafe blob type")
			}
		})
	}
}

func TestReleaseSessionCascadesOnlyLastReference(t *testing.T) {
	s := newTestStore(t)
	first := put(t, s, []byte("shared"))
	second := put(t, s, []byte("shared"))
	third := put(t, s, []byte("shared"))
	if err := s.AddRef(sessionOne, first); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRef(sessionOne, third); err != nil {
		t.Fatal(err)
	}
	if err := s.AddRef(sessionTwo, second); err != nil {
		t.Fatal(err)
	}

	if err := s.ReleaseSession(sessionOne); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.blobPath(first.SHA256)); err != nil {
		t.Fatalf("blob removed while another session owned it: %v", err)
	}
	if got := readCatalog(t, s)[first.SHA256].RefCount; got != 1 {
		t.Fatalf("refcount after first release = %d, want 1", got)
	}

	if err := s.ReleaseSession(sessionTwo); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.blobPath(first.SHA256)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("blob remains after final release: %v", err)
	}
}

func TestReconcileRebuildsIndexesAndGarbageCollects(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.orphanGrace = 0

	d := put(t, s, []byte("reconciled"))
	if err := s.AddRef(sessionOne, d); err != nil {
		t.Fatal(err)
	}
	orphan := put(t, s, []byte("orphan"))
	if err := os.Chtimes(s.blobPath(orphan.SHA256), now.Add(-time.Second), now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	staleStage := filepath.Join(s.stagingDir(), "stale.upload")
	if err := os.WriteFile(staleStage, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(staleStage, now.Add(-stagingMaxAge-time.Second), now.Add(-stagingMaxAge-time.Second)); err != nil {
		t.Fatal(err)
	}

	if err := s.Reconcile(map[string][]Descriptor{sessionTwo: {d}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.sessionPath(sessionOne)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale session index remains: %v", err)
	}
	if got, ok := s.Lookup(sessionTwo, d.ID); !ok || !sameDescriptor(got, d) {
		t.Fatalf("live descriptor not indexed: %+v, %v", got, ok)
	}
	if got := readCatalog(t, s)[d.SHA256].RefCount; got != 1 {
		t.Fatalf("reconciled refcount = %d, want 1", got)
	}
	if _, err := os.Stat(s.blobPath(orphan.SHA256)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("orphan blob remains: %v", err)
	}
	if _, err := os.Stat(staleStage); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale staging file remains: %v", err)
	}
}

func TestReconcileKeepsFreshUnreferencedBlobWithinGrace(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.orphanGrace = time.Hour

	d := put(t, s, []byte("fresh orphan"))
	if err := os.Chtimes(s.blobPath(d.SHA256), now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.blobPath(d.SHA256)); err != nil {
		t.Fatalf("fresh orphan blob was removed: %v", err)
	}
}

func TestRejectsPathInjection(t *testing.T) {
	s := newTestStore(t)
	d := put(t, s, []byte("safe"))

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{
			name: "bad session ID",
			call: func() error { return s.AddRef("../outside", d) },
		},
		{
			name: "bad attachment ID",
			call: func() error {
				bad := d
				bad.ID = "../" + d.ID
				return s.AddRef(sessionOne, bad)
			},
		},
		{
			name: "bad hash",
			call: func() error {
				bad := d
				bad.SHA256 = "../" + d.SHA256
				return s.AddRef(sessionOne, bad)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatal("path injection was accepted")
			}
		})
	}
	if _, _, err := s.Open(sessionOne, "../att_aaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("Open accepted path-injection attachment ID")
	}
}

func TestConcurrentPutsDeduplicate(t *testing.T) {
	s := newTestStore(t)
	const puts = 32
	data := bytes.Repeat([]byte("same concurrent bytes"), 128)
	results := make(chan Descriptor, puts)
	errs := make(chan error, puts)
	var wg sync.WaitGroup
	for range puts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := s.Put(data, PutMeta{Name: "x", Kind: "file"})
			if err != nil {
				errs <- err
				return
			}
			results <- d
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var first Descriptor
	ids := make(map[string]bool)
	for d := range results {
		if first.SHA256 == "" {
			first = d
		}
		if d.SHA256 != first.SHA256 {
			t.Errorf("hash mismatch: %q != %q", d.SHA256, first.SHA256)
		}
		if ids[d.ID] {
			t.Errorf("duplicate occurrence ID %q", d.ID)
		}
		ids[d.ID] = true
	}
	if len(ids) != puts {
		t.Fatalf("successful Put count = %d, want %d", len(ids), puts)
	}
	entries, err := os.ReadDir(filepath.Dir(s.blobPath(first.SHA256)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("blob count = %d, want 1", len(entries))
	}
}

func TestConcurrentPutRefSurvivesSharedSessionRelease(t *testing.T) {
	s := newTestStore(t)
	data := []byte("shared concurrent bytes")
	seed := put(t, s, data)
	if err := s.AddRef(sessionThree, seed); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan struct {
		sessionID string
		d         Descriptor
	}, 2)
	errs := make(chan error, 3)
	var wg sync.WaitGroup
	for _, sessionID := range []string{sessionOne, sessionTwo} {
		wg.Add(1)
		go func(sessionID string) {
			defer wg.Done()
			<-start
			d, err := s.PutRef(sessionID, data, PutMeta{Name: "shared.bin", Kind: "file"})
			if err != nil {
				errs <- err
				return
			}
			results <- struct {
				sessionID string
				d         Descriptor
			}{sessionID: sessionID, d: d}
		}(sessionID)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		if err := s.ReleaseSession(sessionThree); err != nil {
			errs <- err
		}
	}()
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	opened := 0
	for result := range results {
		f, got, err := s.Open(result.sessionID, result.d.ID)
		if err != nil {
			t.Fatalf("PutRef descriptor is not openable: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if !sameDescriptor(got, result.d) {
			t.Fatalf("Open descriptor = %+v, want %+v", got, result.d)
		}
		opened++
	}
	if opened != 2 {
		t.Fatalf("openable PutRef descriptors = %d, want 2", opened)
	}
	if _, err := os.Stat(s.blobPath(seed.SHA256)); err != nil {
		t.Fatalf("shared blob was removed: %v", err)
	}
}

func sameDescriptor(a, b Descriptor) bool {
	return a.ID == b.ID && a.SHA256 == b.SHA256 && a.Name == b.Name &&
		a.Mime == b.Mime && a.Size == b.Size && a.Kind == b.Kind &&
		a.Width == b.Width && a.Height == b.Height && a.CreatedAt.Equal(b.CreatedAt)
}

func TestDefaultBaseDirHonorsConfigDir(t *testing.T) {
	config := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", config)
	got, err := DefaultBaseDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(config, "attachments", "v1")
	if got != want {
		t.Fatalf("DefaultBaseDir = %q, want %q", got, want)
	}
}
