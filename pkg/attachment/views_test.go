package attachment

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestEnsureViewCreatesDurableReadOnlyCopy(t *testing.T) {
	s := newTestStore(t)
	data := []byte("durable view bytes")
	d, err := s.PutRef(sessionOne, data, PutMeta{
		Name: "quarterly report.pdf", Mime: "application/pdf", Kind: "file",
	})
	if err != nil {
		t.Fatal(err)
	}

	view, err := s.EnsureView(sessionOne, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(view) {
		t.Fatalf("view path is not absolute: %q", view)
	}
	if base := filepath.Base(view); base != "quarterly report.pdf" {
		t.Fatalf("view filename = %q, want sanitized descriptor name", base)
	}
	got, err := os.ReadFile(view)
	if err != nil {
		t.Fatal(err)
	}
	blobBytes, err := os.ReadFile(s.blobPath(d.SHA256))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, blobBytes) {
		t.Fatalf("view bytes = %q, want blob bytes %q", got, blobBytes)
	}

	again, err := s.EnsureView(sessionOne, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again != view {
		t.Fatalf("second view path = %q, want %q", again, view)
	}

	viewInfo, err := os.Stat(view)
	if err != nil {
		t.Fatal(err)
	}
	blobInfo, err := os.Stat(s.blobPath(d.SHA256))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(viewInfo, blobInfo) {
		t.Fatal("view shares the blob inode")
	}
	if viewInfo.Mode().Perm() != 0o400 {
		t.Fatalf("view mode = %o, want 0400", viewInfo.Mode().Perm())
	}
}

func TestEnsureViewCopyPreventsCrossSessionBlobCorruption(t *testing.T) {
	s := newTestStore(t)
	original := []byte("shared attachment")
	corrupted := []byte("corrupted content")
	first, err := s.PutRef(sessionOne, original, PutMeta{
		Name: "shared.txt", Mime: "text/plain", Kind: "file",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.PutRef(sessionTwo, original, PutMeta{
		Name: "shared.txt", Mime: "text/plain", Kind: "file",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 != second.SHA256 {
		t.Fatalf("identical attachments use different blobs: %q != %q", first.SHA256, second.SHA256)
	}

	view, err := s.EnsureView(sessionOne, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	viewInfo, err := os.Stat(view)
	if err != nil {
		t.Fatal(err)
	}
	blobPath := s.blobPath(first.SHA256)
	blobInfo, err := os.Stat(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(viewInfo, blobInfo) {
		t.Fatal("session view shares the deduplicated blob inode")
	}

	if err := os.WriteFile(view, corrupted, 0); err != nil && !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("write read-only view error = %v, want permission denied", err)
	}
	// Force a successful tool-style overwrite even when the OS enforced 0400,
	// then verify that the session-local copy cannot affect the shared blob.
	if err := os.Chmod(view, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(view, corrupted, 0); err != nil {
		t.Fatal(err)
	}
	viewBytes, err := os.ReadFile(view)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(viewBytes, corrupted) {
		t.Fatalf("corrupted view bytes = %q, want %q", viewBytes, corrupted)
	}

	blobBytes, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(blobBytes, original) {
		t.Fatalf("stored blob changed through session view: got %q, want %q", blobBytes, original)
	}
	file, _, err := s.Open(sessionTwo, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close() //nolint:errcheck
	secondBytes, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(secondBytes, original) {
		t.Fatalf("session B Open bytes changed through session A view: got %q, want %q", secondBytes, original)
	}
}

func TestEnsureViewRejectsUnownedAndInvalidPaths(t *testing.T) {
	s := newTestStore(t)
	d, err := s.PutRef(sessionOne, []byte("owned"), PutMeta{
		Name: "owned.txt", Mime: "text/plain", Kind: "file",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.EnsureView(sessionTwo, d.ID); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("cross-session EnsureView error = %v, want fs.ErrNotExist", err)
	}
	if _, err := os.Stat(s.sessionViewsDir(sessionTwo)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("cross-session view directory exists or stat failed: %v", err)
	}

	if err := os.RemoveAll(s.viewsDir()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		sessionID string
		attID     string
	}{
		{name: "invalid session", sessionID: "../outside", attID: d.ID},
		{name: "invalid attachment", sessionID: sessionOne, attID: "../" + d.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.EnsureView(tc.sessionID, tc.attID); err == nil {
				t.Fatal("EnsureView accepted an invalid path component")
			}
		})
	}
	if _, err := os.Stat(s.viewsDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("invalid EnsureView touched views directory or stat failed: %v", err)
	}
}

func TestEnsureViewReleaseSessionRemovesViews(t *testing.T) {
	s := newTestStore(t)
	d, err := s.PutRef(sessionOne, []byte("release view"), PutMeta{
		Name: "release.bin", Mime: "application/octet-stream", Kind: "file",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureView(sessionOne, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseSession(sessionOne); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.sessionViewsDir(sessionOne)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("session views remain or stat failed: %v", err)
	}
	if _, err := os.Stat(s.blobPath(d.SHA256)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("released blob remains or stat failed: %v", err)
	}
	if _, err := s.EnsureView(sessionOne, d.ID); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("EnsureView after release error = %v, want fs.ErrNotExist", err)
	}
}

func TestCopyViewIsReadOnly(t *testing.T) {
	s := newTestStore(t)
	data := []byte("copied view")
	d, err := s.PutRef(sessionOne, data, PutMeta{
		Name: "copied.bin", Mime: "application/octet-stream", Kind: "file",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.createViewDirs(sessionOne, d.ID); err != nil {
		t.Fatal(err)
	}
	copyPath := s.viewPath(sessionOne, d.ID, "copied-fallback.bin")
	if err := s.copyView(s.blobPath(d.SHA256), copyPath, d); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("copied view bytes = %q, want %q", got, data)
	}
	copyInfo, err := os.Stat(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	blobInfo, err := os.Stat(s.blobPath(d.SHA256))
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(copyInfo, blobInfo) {
		t.Fatal("copied view shares the blob inode")
	}
	if copyInfo.Mode().Perm()&0o222 != 0 {
		t.Fatalf("copied view mode = %o, want read-only", copyInfo.Mode().Perm())
	}
}

func TestReconcileRemovesOrphanViews(t *testing.T) {
	s := newTestStore(t)
	d, err := s.PutRef(sessionOne, []byte("reconcile view"), PutMeta{
		Name: "reconcile.txt", Mime: "text/plain", Kind: "file",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureView(sessionOne, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Reconcile(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.sessionViewsDir(sessionOne)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("orphan session views remain or stat failed: %v", err)
	}
}

func TestEnsureViewConcurrent(t *testing.T) {
	s := newTestStore(t)
	d, err := s.PutRef(sessionOne, []byte("concurrent view"), PutMeta{
		Name: "concurrent.txt", Mime: "text/plain", Kind: "file",
	})
	if err != nil {
		t.Fatal(err)
	}

	const callers = 2
	paths := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, err := s.EnsureView(sessionOne, d.ID)
			if err != nil {
				errs <- err
				return
			}
			paths <- path
		}()
	}
	wg.Wait()
	close(paths)
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	var first string
	for path := range paths {
		if first == "" {
			first = path
			continue
		}
		if path != first {
			t.Errorf("concurrent view path = %q, want %q", path, first)
		}
	}
	if first == "" {
		return
	}
	if _, err := os.Stat(first); err != nil {
		t.Fatal(err)
	}
}
