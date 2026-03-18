package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMultiEdit_ThreeEditsSuccess(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.go")
	_ = os.WriteFile(file, []byte("a := 1\nb := 2\nc := 3\n"), 0o644)

	ft := NewFileTracker()
	ft.MarkRead(file)

	tool := NewMultiEdit(ToolConfig{WorkspaceRoot: dir, FileTracker: ft, DisableSandbox: true})

	result, err := tool.Execute(nil, map[string]any{
		"path": file,
		"edits": []any{
			map[string]any{"oldText": "a := 1", "newText": "a := 10"},
			map[string]any{"oldText": "b := 2", "newText": "b := 20"},
			map[string]any{"oldText": "c := 3", "newText": "c := 30"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	data, _ := os.ReadFile(file)
	content := string(data)
	if !strings.Contains(content, "a := 10") || !strings.Contains(content, "b := 20") || !strings.Contains(content, "c := 30") {
		t.Errorf("edits not applied: %s", content)
	}
}

func TestMultiEdit_AtomicFailure(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.go")
	original := "a := 1\nb := 2\nc := 3\n"
	_ = os.WriteFile(file, []byte(original), 0o644)

	ft := NewFileTracker()
	ft.MarkRead(file)

	tool := NewMultiEdit(ToolConfig{WorkspaceRoot: dir, FileTracker: ft, DisableSandbox: true})

	result, err := tool.Execute(nil, map[string]any{
		"path": file,
		"edits": []any{
			map[string]any{"oldText": "a := 1", "newText": "a := 10"},
			map[string]any{"oldText": "NONEXISTENT\nSTUFF", "newText": "x"},
			map[string]any{"oldText": "c := 3", "newText": "c := 30"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error result")
	}

	// File should be unchanged (atomic)
	data, _ := os.ReadFile(file)
	if string(data) != original {
		t.Errorf("file was modified despite failed edit: %s", string(data))
	}

	// Error should mention edit #2
	msg := result.Content[0].Text
	if !strings.Contains(msg, "edit #2") {
		t.Errorf("expected 'edit #2' in error, got: %s", msg)
	}
}

func TestMultiEdit_SequentialDependency(t *testing.T) {
	// Edit 1 changes text that edit 2 searches for in the new content
	dir := t.TempDir()
	file := filepath.Join(dir, "test.go")
	_ = os.WriteFile(file, []byte("old_func()\n"), 0o644)

	ft := NewFileTracker()
	ft.MarkRead(file)

	tool := NewMultiEdit(ToolConfig{WorkspaceRoot: dir, FileTracker: ft, DisableSandbox: true})

	result, err := tool.Execute(nil, map[string]any{
		"path": file,
		"edits": []any{
			map[string]any{"oldText": "old_func()", "newText": "new_func()"},
			map[string]any{"oldText": "new_func()", "newText": "final_func()"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), "final_func()") {
		t.Errorf("sequential edits not applied: %s", string(data))
	}
}

func TestMultiEdit_FileTrackerCheck(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.go")
	_ = os.WriteFile(file, []byte("x := 1\n"), 0o644)

	ft := NewFileTracker()
	// Deliberately NOT marking as read

	tool := NewMultiEdit(ToolConfig{WorkspaceRoot: dir, FileTracker: ft, DisableSandbox: true})

	result, err := tool.Execute(nil, map[string]any{
		"path": file,
		"edits": []any{
			map[string]any{"oldText": "x := 1", "newText": "x := 2"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error for unread file")
	}
	if !strings.Contains(result.Content[0].Text, "haven't read") {
		t.Errorf("expected FileTracker error, got: %s", result.Content[0].Text)
	}
}

func TestMultiEdit_BeforeWriteCalledOnce(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.go")
	_ = os.WriteFile(file, []byte("a := 1\nb := 2\n"), 0o644)

	ft := NewFileTracker()
	ft.MarkRead(file)

	callCount := 0
	tool := NewMultiEdit(ToolConfig{
		WorkspaceRoot:  dir,
		FileTracker:    ft,
		DisableSandbox: true,
		BeforeWrite: func(path string) error {
			callCount++
			return nil
		},
	})

	result, _ := tool.Execute(nil, map[string]any{
		"path": file,
		"edits": []any{
			map[string]any{"oldText": "a := 1", "newText": "a := 10"},
			map[string]any{"oldText": "b := 2", "newText": "b := 20"},
		},
	}, nil)
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	if callCount != 1 {
		t.Errorf("BeforeWrite called %d times, expected 1", callCount)
	}
}

func TestMultiEdit_ReplaceAll(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.go")
	_ = os.WriteFile(file, []byte("foo\nbar\nfoo\nbaz\nfoo\n"), 0o644)

	ft := NewFileTracker()
	ft.MarkRead(file)

	tool := NewMultiEdit(ToolConfig{WorkspaceRoot: dir, FileTracker: ft, DisableSandbox: true})

	result, _ := tool.Execute(nil, map[string]any{
		"path": file,
		"edits": []any{
			map[string]any{"oldText": "foo", "newText": "qux", "replaceAll": true},
		},
	}, nil)
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	data, _ := os.ReadFile(file)
	if strings.Contains(string(data), "foo") {
		t.Errorf("replaceAll not applied: %s", string(data))
	}
}
