package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilePicker_UpdateActivation(t *testing.T) {
	var fp filePicker
	fp.SetWorkDir(t.TempDir())

	// No @ → inactive
	fp.Update("hello world", 11)
	if fp.active {
		t.Fatal("should not be active without @")
	}

	// @ at start → active
	fp.Update("@", 1)
	if !fp.active {
		t.Fatal("should be active with @")
	}

	// @ mid-text after space → active
	fp.Update("look at @main", 13)
	if !fp.active {
		t.Fatal("should be active with @ after space")
	}

	// @ glued to word (no space before) → inactive
	fp.Update("foo@bar", 7)
	if fp.active {
		t.Fatal("should not be active with @ glued to word")
	}

	// Space in filter → inactive
	fp.Update("@foo bar", 8)
	if fp.active {
		t.Fatal("should not be active with space in filter")
	}
}

func TestFilePicker_FilterFiles(t *testing.T) {
	dir := t.TempDir()
	// Create some files
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "main_test.go"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "readme.md"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "pkg", "core"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "pkg", "core", "types.go"), []byte("x"), 0o644)

	var fp filePicker
	fp.SetWorkDir(dir)

	// Empty filter → all files
	results := fp.filterFiles("")
	if len(results) == 0 {
		t.Fatal("expected results with empty filter")
	}

	// Filter "main" → matches main.go and main_test.go
	results = fp.filterFiles("main")
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results for 'main', got %d", len(results))
	}

	// Filter "types" → matches pkg/core/types.go
	results = fp.filterFiles("types")
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'types', got %d", len(results))
	}
	if results[0].path != filepath.Join("pkg", "core", "types.go") {
		t.Fatalf("unexpected path: %s", results[0].path)
	}
}

func TestTabCompletePath(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "main_test.go"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)

	// Complete "./m" → common prefix "main" (matches main.go and main_test.go)
	text := "edit ./m"
	newText, _, ok := tabCompletePath(text, len(text), dir)
	if !ok {
		t.Fatal("expected completion")
	}
	// The completion preserves "./" prefix
	if newText != "edit ./main" {
		t.Fatalf("expected 'edit ./main', got %q", newText)
	}

	// Complete "./pk" → "./pkg/" (single dir match, preserves ./)
	text2 := "look at ./pk"
	newText2, _, ok2 := tabCompletePath(text2, len(text2), dir)
	if !ok2 {
		t.Fatal("expected completion for ./pk")
	}
	if newText2 != "look at ./pkg/" {
		t.Fatalf("expected 'look at ./pkg/', got %q", newText2)
	}

	// No path-like token → no completion
	text3 := "hello world"
	_, _, ok3 := tabCompletePath(text3, len(text3), dir)
	if ok3 {
		t.Fatal("should not complete non-path text")
	}
}
