package checkpoint

import (
	"errors"
	"io/fs"
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

func TestRestore_RollsBackAllFilesWhenOneRestoreFails(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.txt")
	second := filepath.Join(dir, "second.txt")
	writeFile(t, first, []byte("before first"))
	writeFile(t, second, []byte("before second"))

	s := New(2)
	s.Begin("turn")
	if err := s.Capture(first); err != nil {
		t.Fatal(err)
	}
	if err := s.Capture(second); err != nil {
		t.Fatal(err)
	}
	writeFile(t, first, []byte("agent first"))
	writeFile(t, second, []byte("agent second"))
	s.Commit()
	cp, err := s.Undo()
	if err != nil {
		t.Fatal(err)
	}
	// Keep the order deterministic: first restoration succeeds, then the
	// injected failure proves that first is rolled back as well.
	if cp.Files[0].Path == second {
		cp.Files[0], cp.Files[1] = cp.Files[1], cp.Files[0]
	}
	s.restoreFile = func(path string, content []byte, perm fs.FileMode) error {
		if path == second {
			return errors.New("injected restore failure")
		}
		return atomicWriteFile(path, content, perm)
	}

	if err := s.Restore(cp); err == nil {
		t.Fatal("Restore succeeded despite injected failure")
	}
	for path, want := range map[string]string{first: "agent first", second: "agent second"} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s after failed restore = %q, want %q", path, got, want)
		}
	}
}

func TestRestore_RestoresEveryFileOrDeletesAgentCreatedFiles(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.txt")
	created := filepath.Join(dir, "created.txt")
	writeFile(t, existing, []byte("before"))

	s := New(2)
	s.Begin("turn")
	if err := s.Capture(existing); err != nil {
		t.Fatal(err)
	}
	if err := s.Capture(created); err != nil {
		t.Fatal(err)
	}
	writeFile(t, existing, []byte("agent"))
	writeFile(t, created, []byte("created"))
	s.Commit()
	cp, err := s.Undo()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(cp); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "before" {
		t.Errorf("existing = %q, want original content", got)
	}
	if _, err := os.Stat(created); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("created file remains after restore: %v", err)
	}
}

func TestUndoAndRestore_MixedMultiFileCheckpoint(t *testing.T) {
	dir := t.TempDir()
	updated := filepath.Join(dir, "updated.txt")
	deleted := filepath.Join(dir, "deleted.txt")
	created := filepath.Join(dir, "created.txt")
	writeFile(t, updated, []byte("before update"))
	writeFile(t, deleted, []byte("before delete"))

	s := New(2)
	s.Begin("turn")
	for _, path := range []string{updated, deleted, created} {
		if err := s.Capture(path); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, updated, []byte("agent update"))
	if err := os.Remove(deleted); err != nil {
		t.Fatal(err)
	}
	writeFile(t, created, []byte("agent created"))
	s.Commit()

	if err := s.UndoAndRestore(); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{updated: "before update", deleted: "before delete"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", path, got, err, want)
		}
	}
	if _, err := os.Stat(created); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("agent-created file remains: %v", err)
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

func TestSnapshot_ModifiedSinceCapture_DetectsExternalEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeFile(t, path, []byte("original"))

	s := New(20)
	s.Begin("turn")
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, []byte("agent-written")) // agent's own edit
	s.Commit()

	cp, err := s.Undo()
	if err != nil {
		t.Fatal(err)
	}
	snap := cp.Files[0]
	if snap.ModifiedSinceCapture() {
		t.Error("expected file left untouched by agent to not be flagged as modified")
	}

	// Different size guarantees detection regardless of mtime resolution.
	writeFile(t, path, []byte("an external edit with a different length"))
	if !snap.ModifiedSinceCapture() {
		t.Error("expected external edit after agent's turn to be flagged as modified")
	}
}

func TestSnapshot_ModifiedSinceCapture_AgentCreatedFileUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	s := New(20)
	s.Begin("turn")
	if err := s.Capture(path); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, []byte("created by agent"))
	s.Commit()

	cp, err := s.Undo()
	if err != nil {
		t.Fatal(err)
	}
	snap := cp.Files[0]
	if snap.ModifiedSinceCapture() {
		t.Error("expected agent-created file to not be flagged as modified")
	}

	// Different size guarantees detection regardless of mtime resolution.
	writeFile(t, path, []byte("an external edit with a different length"))
	if !snap.ModifiedSinceCapture() {
		t.Error("expected external edit after agent created the file to be flagged as modified")
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
