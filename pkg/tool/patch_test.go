package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

func newPatchTool(t *testing.T, dir string, ft *FileTracker) core.Tool {
	t.Helper()
	return NewApplyPatch(ToolConfig{
		WorkspaceRoot:  dir,
		FileTracker:    ft,
		DisableSandbox: true,
	})
}

func TestPatch_AddNewFile(t *testing.T) {
	dir := t.TempDir()
	tool := newPatchTool(t, dir, nil)

	patch := `*** Begin Patch
*** Add File: hello.txt
+Hello world
+Second line
*** End Patch`

	result, err := tool.Execute(nil, map[string]any{"patch": patch}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	data, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Hello world") {
		t.Errorf("file content mismatch: %s", string(data))
	}
}

func TestPatch_UpdateExistingFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	_ = os.WriteFile(file, []byte("func main() {\n\told()\n}\n"), 0o644)

	ft := NewFileTracker()
	ft.MarkRead(file)
	tool := newPatchTool(t, dir, ft)

	patch := `*** Begin Patch
*** Update File: main.go
@@ func main()
-	old()
+	new()
*** End Patch`

	result, err := tool.Execute(nil, map[string]any{"patch": patch}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), "\tnew()") {
		t.Errorf("update not applied: %s", string(data))
	}
}

func TestPatch_DeleteFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "gone.txt")
	_ = os.WriteFile(file, []byte("bye\n"), 0o644)

	ft := NewFileTracker()
	ft.MarkRead(file)
	tool := newPatchTool(t, dir, ft)

	patch := `*** Begin Patch
*** Delete File: gone.txt
*** End Patch`

	result, err := tool.Execute(nil, map[string]any{"patch": patch}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("file should be deleted")
	}
}

func TestPatch_MoveFile(t *testing.T) {
	dir := t.TempDir()
	oldFile := filepath.Join(dir, "old.go")
	_ = os.WriteFile(oldFile, []byte("package old\n\nfunc Foo() {}\n"), 0o644)

	ft := NewFileTracker()
	ft.MarkRead(oldFile)
	tool := newPatchTool(t, dir, ft)

	patch := `*** Begin Patch
*** Update File: old.go
*** Move to: new.go
@@ package old
-package old
+package new
*** End Patch`

	result, err := tool.Execute(nil, map[string]any{"patch": patch}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	// Old file should be gone
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Error("old file should be deleted")
	}

	// New file should exist with updated content
	data, err := os.ReadFile(filepath.Join(dir, "new.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "package new") {
		t.Errorf("move content mismatch: %s", string(data))
	}
}

func TestPatch_MultiFileAtomic(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "a.go")
	file2 := filepath.Join(dir, "b.go")
	_ = os.WriteFile(file1, []byte("package a\n"), 0o644)
	_ = os.WriteFile(file2, []byte("package b\n"), 0o644)

	ft := NewFileTracker()
	ft.MarkRead(file1)
	ft.MarkRead(file2)
	tool := newPatchTool(t, dir, ft)

	patch := `*** Begin Patch
*** Update File: a.go
-package a
+package aa
*** Update File: b.go
-package b
+package bb
*** End Patch`

	result, err := tool.Execute(nil, map[string]any{"patch": patch}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	d1, _ := os.ReadFile(file1)
	d2, _ := os.ReadFile(file2)
	if !strings.Contains(string(d1), "package aa") {
		t.Errorf("a.go not updated: %s", string(d1))
	}
	if !strings.Contains(string(d2), "package bb") {
		t.Errorf("b.go not updated: %s", string(d2))
	}
}

func TestPatch_ValidationFailureNoChanges(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "exists.go")
	original := "package exists\n"
	_ = os.WriteFile(file, []byte(original), 0o644)

	ft := NewFileTracker()
	ft.MarkRead(file)
	tool := newPatchTool(t, dir, ft)

	// Second hunk refers to nonexistent file for update → validation failure
	patch := `*** Begin Patch
*** Update File: exists.go
-package exists
+package changed
*** Update File: nonexistent.go
-something
+else
*** End Patch`

	result, err := tool.Execute(nil, map[string]any{"patch": patch}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error for invalid hunk")
	}

	// First file should NOT have been modified (transactional)
	data, _ := os.ReadFile(file)
	if string(data) != original {
		t.Errorf("file was modified despite validation failure: %s", string(data))
	}
}

func TestPatch_FileTrackerCheck(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "unread.go")
	_ = os.WriteFile(file, []byte("package unread\n"), 0o644)

	ft := NewFileTracker()
	// NOT marking as read
	tool := newPatchTool(t, dir, ft)

	patch := `*** Begin Patch
*** Update File: unread.go
-package unread
+package read
*** End Patch`

	result, err := tool.Execute(nil, map[string]any{"patch": patch}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error for unread file")
	}
	if !strings.Contains(result.Content[0].Text, "hasn't been read") {
		t.Errorf("expected FileTracker error, got: %s", result.Content[0].Text)
	}
}

func TestPatch_SameFileSequentialHunks(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	_ = os.WriteFile(file, []byte("line1\nline2\nline3\n"), 0o644)

	ft := NewFileTracker()
	ft.MarkRead(file)
	tool := newPatchTool(t, dir, ft)

	// Two hunks on same file — second should see result of first
	patch := `*** Begin Patch
*** Update File: main.go
-line1
+LINE1
*** Update File: main.go
-line3
+LINE3
*** End Patch`

	result, err := tool.Execute(nil, map[string]any{"patch": patch}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	data, _ := os.ReadFile(file)
	content := string(data)
	if !strings.Contains(content, "LINE1") || !strings.Contains(content, "LINE3") {
		t.Errorf("sequential hunks not applied correctly: %s", content)
	}
}

func TestPatch_AddExistingFileRejected(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "exists.txt")
	_ = os.WriteFile(file, []byte(""), 0o644) // empty file

	tool := newPatchTool(t, dir, nil)

	patch := `*** Begin Patch
*** Add File: exists.txt
+new content
*** End Patch`

	result, err := tool.Execute(nil, map[string]any{"patch": patch}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error for add on existing file")
	}
	if !strings.Contains(result.Content[0].Text, "already exists") {
		t.Errorf("expected 'already exists' error, got: %s", result.Content[0].Text)
	}
}

func TestPatch_MalformedAddFileContent(t *testing.T) {
	patch := `*** Begin Patch
*** Add File: test.txt
+valid line
this is not prefixed
*** End Patch`
	_, err := ParsePatch(patch)
	if err == nil {
		t.Fatal("expected parse error for non-'+' line in Add File")
	}
	if !strings.Contains(err.Error(), "unexpected line") {
		t.Errorf("expected 'unexpected line' error, got: %v", err)
	}
}

func TestPatch_EmptyUpdateChunks(t *testing.T) {
	patch := `*** Begin Patch
*** Update File: test.go
*** End Patch`
	_, err := ParsePatch(patch)
	if err == nil {
		t.Fatal("expected parse error for Update with no chunks")
	}
	if !strings.Contains(err.Error(), "no chunks") {
		t.Errorf("expected 'no chunks' error, got: %v", err)
	}
}

func TestPatch_BeforeWriteCalledPerFile(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.go")
	f2 := filepath.Join(dir, "b.go")
	_ = os.WriteFile(f1, []byte("aaa\n"), 0o644)
	_ = os.WriteFile(f2, []byte("bbb\n"), 0o644)

	ft := NewFileTracker()
	ft.MarkRead(f1)
	ft.MarkRead(f2)

	var paths []string
	tool := NewApplyPatch(ToolConfig{
		WorkspaceRoot:  dir,
		FileTracker:    ft,
		DisableSandbox: true,
		BeforeWrite: func(path string) error {
			paths = append(paths, path)
			return nil
		},
	})

	patch := `*** Begin Patch
*** Update File: a.go
-aaa
+AAA
*** Update File: b.go
-bbb
+BBB
*** End Patch`

	result, _ := tool.Execute(nil, map[string]any{"patch": patch}, nil)
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	if len(paths) != 2 {
		t.Errorf("BeforeWrite called %d times, expected 2. paths=%v", len(paths), paths)
	}
}
