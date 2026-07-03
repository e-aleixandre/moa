package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicWriteFile_WritesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := atomicWriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Errorf("content mismatch: %q", string(data))
	}
}

func TestAtomicWriteFile_PreservesPermOnOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "script.sh")

	if err := atomicWriteFile(path, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Rewrite using the current mode as fileModeOr would.
	if err := atomicWriteFile(path, []byte("v2"), fileModeOr(path, 0o644)); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("perm not preserved: got %o want 0755", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if string(data) != "v2" {
		t.Errorf("content mismatch: %q", string(data))
	}
}

func TestAtomicWriteFile_NoTempLeftOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")

	if err := atomicWriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".moa-write-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestAtomicWriteFile_MissingDirErrorsNoTemp(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope")
	path := filepath.Join(missing, "out.txt")

	if err := atomicWriteFile(path, []byte("hello"), 0o644); err == nil {
		t.Fatal("expected error for nonexistent directory")
	}

	// The nonexistent dir must not have been created with a stray temp.
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("expected missing dir to stay absent, err=%v", err)
	}
}

func TestFileModeOr(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := fileModeOr(path, 0o644); got != 0o600 {
		t.Errorf("existing file: got %o want 0600", got)
	}

	missing := filepath.Join(dir, "missing.txt")
	if got := fileModeOr(missing, 0o644); got != 0o644 {
		t.Errorf("missing file: got %o want default 0644", got)
	}
}

func TestEdit_PreservesExecutableMode(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(file, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	ft := NewFileTracker()
	ft.MarkRead(file)
	tool := NewEdit(ToolConfig{WorkspaceRoot: dir, FileTracker: ft, DisableSandbox: true})

	result, err := tool.Execute(nil, map[string]any{
		"path":    file,
		"oldText": "echo old",
		"newText": "echo new",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("executable bit lost: got %o want 0755", info.Mode().Perm())
	}
	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), "echo new") {
		t.Errorf("edit not applied: %s", string(data))
	}
}
