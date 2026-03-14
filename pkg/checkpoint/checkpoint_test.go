package checkpoint

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// writeFile is a test helper that writes a file or fails the test.
func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCapture_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeFile(t, path, []byte("original"))

	s := New(5)
	s.Begin("turn 1")
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, []byte("modified"))
	s.Commit()

	cp, err := s.Undo()
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(cp.Files))
	}
	if string(cp.Files[0].Content) != "original" {
		t.Errorf("expected 'original', got %q", cp.Files[0].Content)
	}

	writeFile(t, path, cp.Files[0].Content)
	got, _ := os.ReadFile(path)
	if string(got) != "original" {
		t.Errorf("after restore: expected 'original', got %q", got)
	}
}

func TestCapture_NewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	s := New(5)
	s.Begin("turn 1")
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, []byte("created"))
	s.Commit()

	cp, err := s.Undo()
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(cp.Files))
	}
	if cp.Files[0].Content != nil {
		t.Errorf("expected nil content for new file, got %q", cp.Files[0].Content)
	}

	_ = os.Remove(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected file to not exist after undo")
	}
}

func TestCapture_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeFile(t, path, []byte("v1"))

	s := New(5)
	s.Begin("turn")
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, []byte("v2"))
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	s.Commit()

	cp, _ := s.Undo()
	if string(cp.Files[0].Content) != "v1" {
		t.Errorf("expected 'v1', got %q", cp.Files[0].Content)
	}
}

func TestCapture_NoActiveCheckpoint(t *testing.T) {
	s := New(5)
	err := s.Capture("/some/path")
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestCapture_IOError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "noread")
	writeFile(t, path, []byte("data"))
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(path, 0o644) }()

	s := New(5)
	s.Begin("turn")
	err := s.Capture(path)
	if err == nil {
		t.Error("expected error for unreadable file")
	}
}

func TestUndo_Empty(t *testing.T) {
	s := New(5)
	_, err := s.Undo()
	if err == nil {
		t.Error("expected error for empty store")
	}
}

func TestUndo_WithActiveCheckpoint(t *testing.T) {
	s := New(5)
	s.Begin("turn")
	_, err := s.Undo()
	if err == nil {
		t.Error("expected error when checkpoint is active")
	}
}

func TestCircularBuffer(t *testing.T) {
	dir := t.TempDir()
	s := New(3)

	for i := 0; i < 5; i++ {
		path := filepath.Join(dir, "file.txt")
		writeFile(t, path, []byte("before"))
		s.Begin("turn " + string(rune('A'+i)))
		if err := s.Capture(path); err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, []byte("after"))
		s.Commit()
	}

	list := s.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 checkpoints, got %d", len(list))
	}
	if list[0].Label != "turn E" {
		t.Errorf("expected newest 'turn E', got %q", list[0].Label)
	}
	if list[2].Label != "turn C" {
		t.Errorf("expected oldest 'turn C', got %q", list[2].Label)
	}

	for i := 0; i < 3; i++ {
		if _, err := s.Undo(); err != nil {
			t.Fatalf("undo %d failed: %v", i+1, err)
		}
	}
	if _, err := s.Undo(); err == nil {
		t.Error("expected error after all checkpoints undone")
	}
}

func TestConcurrentCapture(t *testing.T) {
	dir := t.TempDir()
	s := New(5)
	s.Begin("concurrent")

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			path := filepath.Join(dir, "file"+string(rune('0'+n))+".txt")
			_ = os.WriteFile(path, []byte("data"), 0o644)
			_ = s.Capture(path)
		}(i)
	}
	wg.Wait()
	s.Commit()

	cp, err := s.Undo()
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Files) != 10 {
		t.Errorf("expected 10 files, got %d", len(cp.Files))
	}
}

func TestCommit_NoFiles(t *testing.T) {
	s := New(5)
	s.Begin("empty")
	s.Commit()

	list := s.List()
	if len(list) != 0 {
		t.Errorf("expected 0 checkpoints, got %d", len(list))
	}
}

func TestDiscard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeFile(t, path, []byte("data"))

	s := New(5)
	s.Begin("discarded")
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	s.Discard()

	list := s.List()
	if len(list) != 0 {
		t.Errorf("expected 0 checkpoints after discard, got %d", len(list))
	}

	_, err := s.Undo()
	if err == nil {
		t.Error("expected error after discard")
	}
}
