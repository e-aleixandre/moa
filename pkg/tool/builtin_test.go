package tool

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ealeixandre/go-agent/pkg/core"
)

func TestSafePath_Normal(t *testing.T) {
	tmp := t.TempDir()

	p, err := safePath(tmp, "subdir/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(tmp, "subdir/file.txt")
	if p != expected {
		t.Fatalf("expected %s, got %s", expected, p)
	}
}

func TestSafePath_EscapeDetected(t *testing.T) {
	tmp := t.TempDir()

	_, err := safePath(tmp, "../../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path escape")
	}
}

func TestSafePath_AbsoluteOutside(t *testing.T) {
	tmp := t.TempDir()

	_, err := safePath(tmp, "/etc/passwd")
	if err == nil {
		t.Fatal("expected error for absolute path outside workspace")
	}
}

func TestSafePath_SymlinkEscape(t *testing.T) {
	tmp := t.TempDir()
	outside := t.TempDir()

	// Create a symlink inside workspace pointing outside
	linkPath := filepath.Join(tmp, "escape")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Skip("cannot create symlinks")
	}

	_, err := safePath(tmp, "escape/secret.txt")
	if err == nil {
		t.Fatal("expected error for symlink escape")
	}
}

func TestSafePath_NoRoot(t *testing.T) {
	// Empty root means no restriction
	p, err := safePath("", "/tmp/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if p != "/tmp/file.txt" {
		t.Fatalf("expected /tmp/file.txt, got %s", p)
	}
}

func TestRegisterBuiltins(t *testing.T) {
	reg := core.NewRegistry()
	tmp := t.TempDir()
	RegisterBuiltins(reg, ToolConfig{WorkspaceRoot: tmp})

	expected := []string{"bash", "read", "write", "edit", "grep", "find", "ls"}
	for _, name := range expected {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("missing tool: %s", name)
		}
	}
	if reg.Count() != len(expected) {
		t.Fatalf("expected %d tools, got %d", len(expected), reg.Count())
	}
}

func TestBash_Simple(t *testing.T) {
	tmp := t.TempDir()
	bash := NewBash(ToolConfig{WorkspaceRoot: tmp})

	result, err := bash.Execute(context.Background(), map[string]any{"command": "echo hello"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) == 0 || result.Content[0].Text == "" {
		t.Fatal("expected output")
	}
	if result.Content[0].Text != "hello\n" {
		t.Fatalf("expected 'hello\\n', got %q", result.Content[0].Text)
	}
}

func TestBash_ExitCode(t *testing.T) {
	tmp := t.TempDir()
	bash := NewBash(ToolConfig{WorkspaceRoot: tmp})

	result, err := bash.Execute(context.Background(), map[string]any{"command": "exit 42"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].Text
	if !contains(text, "Exit code: 42") {
		t.Fatalf("expected exit code in output: %q", text)
	}
}

func TestBash_ContextCancel(t *testing.T) {
	tmp := t.TempDir()
	bash := NewBash(ToolConfig{WorkspaceRoot: tmp})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result, err := bash.Execute(ctx, map[string]any{"command": "sleep 60"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Should not hang — should return quickly with error or timeout message
	_ = result
}

func TestRead_TextFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.txt")
	os.WriteFile(path, []byte("line1\nline2\nline3"), 0o644)

	read := NewRead(ToolConfig{WorkspaceRoot: tmp})
	result, err := read.Execute(context.Background(), map[string]any{"path": "test.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].Text
	if !contains(text, "line1") || !contains(text, "line3") {
		t.Fatalf("expected file content: %q", text)
	}
}

func TestRead_WithOffset(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.txt")
	os.WriteFile(path, []byte("line1\nline2\nline3\nline4"), 0o644)

	read := NewRead(ToolConfig{WorkspaceRoot: tmp})
	result, err := read.Execute(context.Background(), map[string]any{
		"path":   "test.txt",
		"offset": float64(2),
		"limit":  float64(2),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].Text
	if !contains(text, "line2") || !contains(text, "line3") {
		t.Fatalf("expected lines 2-3: %q", text)
	}
}

func TestRead_NotFound(t *testing.T) {
	tmp := t.TempDir()
	read := NewRead(ToolConfig{WorkspaceRoot: tmp})
	result, err := read.Execute(context.Background(), map[string]any{"path": "nonexistent.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result.Content[0].Text, "Error") {
		t.Fatal("expected error result for missing file")
	}
}

func TestWrite_CreateFile(t *testing.T) {
	tmp := t.TempDir()
	write := NewWrite(ToolConfig{WorkspaceRoot: tmp})

	result, err := write.Execute(context.Background(), map[string]any{
		"path":    "subdir/new.txt",
		"content": "hello world",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result.Content[0].Text, "Wrote") {
		t.Fatalf("expected success: %q", result.Content[0].Text)
	}

	data, err := os.ReadFile(filepath.Join(tmp, "subdir/new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world" {
		t.Fatalf("wrong content: %q", string(data))
	}
}

func TestEdit_ReplaceExact(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.go")
	os.WriteFile(path, []byte("func main() {\n\tfmt.Println(\"hello\")\n}"), 0o644)

	edit := NewEdit(ToolConfig{WorkspaceRoot: tmp})
	result, err := edit.Execute(context.Background(), map[string]any{
		"path":    "file.go",
		"oldText": "\"hello\"",
		"newText": "\"world\"",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(result.Content[0].Text, "Edited") {
		t.Fatalf("expected success: %q", result.Content[0].Text)
	}

	data, _ := os.ReadFile(path)
	if !contains(string(data), "\"world\"") {
		t.Fatalf("edit not applied: %s", string(data))
	}
}

func TestEdit_NotFound(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")
	os.WriteFile(path, []byte("hello"), 0o644)

	edit := NewEdit(ToolConfig{WorkspaceRoot: tmp})
	result, _ := edit.Execute(context.Background(), map[string]any{
		"path":    "file.txt",
		"oldText": "nonexistent",
		"newText": "replacement",
	}, nil)
	if !contains(result.Content[0].Text, "not found") {
		t.Fatalf("expected not found: %q", result.Content[0].Text)
	}
}

func TestEdit_MultipleMatches(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "file.txt")
	os.WriteFile(path, []byte("hello hello hello"), 0o644)

	edit := NewEdit(ToolConfig{WorkspaceRoot: tmp})
	result, _ := edit.Execute(context.Background(), map[string]any{
		"path":    "file.txt",
		"oldText": "hello",
		"newText": "world",
	}, nil)
	if !contains(result.Content[0].Text, "3 locations") {
		t.Fatalf("expected multiple match error: %q", result.Content[0].Text)
	}
}

func TestLs_Directory(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(tmp, "b.txt"), []byte("b"), 0o644)
	os.Mkdir(filepath.Join(tmp, "subdir"), 0o755)

	ls := NewLs(ToolConfig{WorkspaceRoot: tmp})
	result, err := ls.Execute(context.Background(), map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].Text
	if !contains(text, "a.txt") || !contains(text, "b.txt") || !contains(text, "subdir/") {
		t.Fatalf("expected directory listing: %q", text)
	}
}

func TestBash_CwdEscape(t *testing.T) {
	tmp := t.TempDir()
	bash := NewBash(ToolConfig{WorkspaceRoot: tmp})

	result, err := bash.Execute(context.Background(), map[string]any{
		"command": "pwd",
		"cwd":     "../../etc",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("expected error result for cwd escape, got: %q", result.Content[0].Text)
	}
}

func TestBash_AbsoluteCwdEscape(t *testing.T) {
	tmp := t.TempDir()
	bash := NewBash(ToolConfig{WorkspaceRoot: tmp})

	result, err := bash.Execute(context.Background(), map[string]any{
		"command": "pwd",
		"cwd":     "/etc",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("expected error result for absolute cwd escape, got: %q", result.Content[0].Text)
	}
}

func TestGrep_PathEscape(t *testing.T) {
	tmp := t.TempDir()
	grep := NewGrep(ToolConfig{WorkspaceRoot: tmp})

	result, err := grep.Execute(context.Background(), map[string]any{
		"pattern": "root",
		"path":    "../../etc",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("expected error result for path escape, got: %q", result.Content[0].Text)
	}
}

func TestFind_PathEscape(t *testing.T) {
	tmp := t.TempDir()
	find := NewFind(ToolConfig{WorkspaceRoot: tmp})

	result, err := find.Execute(context.Background(), map[string]any{
		"pattern": "*",
		"path":    "/etc",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("expected error result for path escape, got: %q", result.Content[0].Text)
	}
}

func TestGrep_DefaultPath(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "test.txt"), []byte("hello world"), 0o644)

	grep := NewGrep(ToolConfig{WorkspaceRoot: tmp})

	// No path param → should use workspace root
	result, err := grep.Execute(context.Background(), map[string]any{
		"pattern": "hello",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected successful grep, got error: %q", result.Content[0].Text)
	}
}

func TestFind_DefaultPath(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "test.txt"), []byte("hello"), 0o644)

	find := NewFind(ToolConfig{WorkspaceRoot: tmp})

	// No path param → should use "." (workspace root via cmd.Dir)
	result, err := find.Execute(context.Background(), map[string]any{
		"pattern": "test.txt",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected successful find, got error: %q", result.Content[0].Text)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
