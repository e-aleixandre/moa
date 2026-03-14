package checkpoint

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestCapture_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("original"), 0o644)

	s := New(5)
	s.Begin("turn 1")
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	// Simulate the write tool changing the file.
	os.WriteFile(path, []byte("modified"), 0o644)
	s.Commit()

	// Undo should return the original content.
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

	// Restore the file manually (as the /undo command would).
	os.WriteFile(path, cp.Files[0].Content, cp.Files[0].Perm)
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
	// Simulate the write tool creating the file.
	os.WriteFile(path, []byte("created"), 0o644)
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

	// Undo should delete the file.
	os.Remove(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected file to not exist after undo")
	}
}

func TestCapture_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("v1"), 0o644)

	s := New(5)
	s.Begin("turn")
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	// Modify the file between captures (simulating edit then write).
	os.WriteFile(path, []byte("v2"), 0o644)
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	s.Commit()

	cp, _ := s.Undo()
	// Should have captured the original "v1", not the intermediate "v2".
	if string(cp.Files[0].Content) != "v1" {
		t.Errorf("expected 'v1', got %q", cp.Files[0].Content)
	}
}

func TestCapture_NoActiveCheckpoint(t *testing.T) {
	s := New(5)
	// Capture without Begin should be a no-op.
	err := s.Capture("/some/path")
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestCapture_IOError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "noread")
	os.WriteFile(path, []byte("data"), 0o000)
	defer os.Chmod(path, 0o644) // cleanup

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
		os.WriteFile(path, []byte("before"), 0o644)
		s.Begin("turn " + string(rune('A'+i)))
		s.Capture(path)
		os.WriteFile(path, []byte("after"), 0o644)
		s.Commit()
	}

	// Only 3 checkpoints should be retained (the last 3).
	list := s.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 checkpoints, got %d", len(list))
	}

	// Newest first.
	if list[0].Label != "turn E" {
		t.Errorf("expected newest 'turn E', got %q", list[0].Label)
	}
	if list[2].Label != "turn C" {
		t.Errorf("expected oldest 'turn C', got %q", list[2].Label)
	}

	// Should be able to undo 3 times, then error.
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
			os.WriteFile(path, []byte("data"), 0o644)
			s.Capture(path)
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
	s.Commit() // no captures → should not create a checkpoint

	list := s.List()
	if len(list) != 0 {
		t.Errorf("expected 0 checkpoints, got %d", len(list))
	}
}

func TestDiscard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	os.WriteFile(path, []byte("data"), 0o644)

	s := New(5)
	s.Begin("discarded")
	s.Capture(path)
	s.Discard()

	// No checkpoint should have been saved.
	list := s.List()
	if len(list) != 0 {
		t.Errorf("expected 0 checkpoints after discard, got %d", len(list))
	}

	// Undo should fail.
	_, err := s.Undo()
	if err == nil {
		t.Error("expected error after discard")
	}
}
