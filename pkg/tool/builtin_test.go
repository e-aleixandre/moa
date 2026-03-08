package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ealeixandre/moa/pkg/core"
)

func cfgWith(root string) ToolConfig {
	return ToolConfig{WorkspaceRoot: root}
}

func TestSafePath_Normal(t *testing.T) {
	tmp := t.TempDir()

	p, err := safePath(cfgWith(tmp), "subdir/file.txt")
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

	_, err := safePath(cfgWith(tmp), "../../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path escape")
	}
}

func TestSafePath_AbsoluteOutside(t *testing.T) {
	tmp := t.TempDir()

	_, err := safePath(cfgWith(tmp), "/etc/passwd")
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

	_, err := safePath(cfgWith(tmp), "escape/secret.txt")
	if err == nil {
		t.Fatal("expected error for symlink escape")
	}
}

func TestSafePath_NoRoot(t *testing.T) {
	// Empty root means no restriction
	p, err := safePath(ToolConfig{}, "/tmp/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if p != "/tmp/file.txt" {
		t.Fatalf("expected /tmp/file.txt, got %s", p)
	}
}

func TestSafePath_DisableSandbox(t *testing.T) {
	tmp := t.TempDir()
	cfg := ToolConfig{WorkspaceRoot: tmp, DisableSandbox: true}

	p, err := safePath(cfg, "/etc/passwd")
	if err != nil {
		t.Fatal("DisableSandbox should allow any path")
	}
	if p != "/etc/passwd" {
		t.Fatalf("expected /etc/passwd, got %s", p)
	}
}

func TestSafePath_AllowedPaths(t *testing.T) {
	tmp := t.TempDir()
	outside := t.TempDir()
	cfg := ToolConfig{WorkspaceRoot: tmp, AllowedPaths: []string{outside}}

	// Path inside allowed dir should work
	target := filepath.Join(outside, "file.txt")
	p, err := safePath(cfg, target)
	if err != nil {
		t.Fatalf("AllowedPaths should permit %s: %v", target, err)
	}
	if p != target {
		t.Fatalf("expected %s, got %s", target, p)
	}

	// Path outside both workspace and allowed dirs should fail
	_, err = safePath(cfg, "/etc/passwd")
	if err == nil {
		t.Fatal("should reject paths outside workspace and AllowedPaths")
	}
}

func TestRegisterBuiltins(t *testing.T) {
	reg := core.NewRegistry()
	tmp := t.TempDir()
	RegisterBuiltins(reg, ToolConfig{WorkspaceRoot: tmp})

	expected := []string{"bash", "read", "write", "edit", "grep", "find", "ls", "fetch_content"}
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
	if !strings.Contains(text, "Exit code: 42") {
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
	if !strings.Contains(text, "line1") || !strings.Contains(text, "line3") {
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
	if !strings.Contains(text, "line2") || !strings.Contains(text, "line3") {
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
	if !strings.Contains(result.Content[0].Text, "Error") {
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
	if !strings.Contains(result.Content[0].Text, "Wrote") {
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
	if !strings.Contains(result.Content[0].Text, "Edited") {
		t.Fatalf("expected success: %q", result.Content[0].Text)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "\"world\"") {
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
	if !strings.Contains(result.Content[0].Text, "not found") {
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
	if !strings.Contains(result.Content[0].Text, "3 locations") {
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
	if !strings.Contains(text, "a.txt") || !strings.Contains(text, "b.txt") || !strings.Contains(text, "subdir/") {
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

func TestGrep_DashPattern(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "test.txt"), []byte("hello -test world\n"), 0o644)

	grep := NewGrep(ToolConfig{WorkspaceRoot: tmp})
	result, err := grep.Execute(context.Background(), map[string]any{
		"pattern":       "-test",
		"fixed_strings": true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("pattern starting with dash should not error: %q", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "-test") {
		t.Fatalf("expected match, got: %q", result.Content[0].Text)
	}
}

func TestFind_DashPattern(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "-test.txt"), []byte("x"), 0o644)

	find := NewFind(ToolConfig{WorkspaceRoot: tmp})
	result, err := find.Execute(context.Background(), map[string]any{
		"pattern": "-test*",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("pattern starting with dash should not error: %q", result.Content[0].Text)
	}
}

func TestRead_ExactLimitLines(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.txt")
	os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644)

	read := NewRead(ToolConfig{WorkspaceRoot: tmp})
	result, err := read.Execute(context.Background(), map[string]any{
		"path":  "test.txt",
		"limit": float64(3),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].Text
	if strings.Contains(text, "truncated") {
		t.Fatalf("file with exactly limit lines should NOT show truncation: %q", text)
	}
}

func TestRead_EmptyFileWithOffset(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "empty.txt")
	os.WriteFile(path, []byte(""), 0o644)

	read := NewRead(ToolConfig{WorkspaceRoot: tmp})
	result, err := read.Execute(context.Background(), map[string]any{
		"path":   "empty.txt",
		"offset": float64(5),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Empty file — should not panic, just return empty or "past end"
	_ = result
}

func TestRead_OffsetPastEOF(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.txt")
	os.WriteFile(path, []byte("line1\nline2\n"), 0o644)

	read := NewRead(ToolConfig{WorkspaceRoot: tmp})
	result, err := read.Execute(context.Background(), map[string]any{
		"path":   "test.txt",
		"offset": float64(100),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "past end") {
		t.Fatalf("expected 'past end' message, got: %q", text)
	}
}

func TestRead_UTF8Boundary(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.txt")
	// Write content that, after byte truncation, might cut a multi-byte char.
	// Each '€' is 3 bytes (U+20AC). Fill up to just over maxOutputBytes.
	euro := strings.Repeat("€", (maxOutputBytes/3)+10)
	os.WriteFile(path, []byte(euro), 0o644)

	read := NewRead(ToolConfig{WorkspaceRoot: tmp})
	result, err := read.Execute(context.Background(), map[string]any{
		"path":  "test.txt",
		"limit": float64(maxOutputLines),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].Text
	// The output must be valid UTF-8 (truncation walked back to boundary)
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && size <= 1 {
			t.Fatalf("invalid UTF-8 at byte %d in output", i)
		}
		i += size
	}
}

func TestRead_LongLine(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.txt")
	// Write a single line longer than maxOutputBytes
	long := strings.Repeat("x", maxOutputBytes+1000)
	os.WriteFile(path, []byte(long), 0o644)

	read := NewRead(ToolConfig{WorkspaceRoot: tmp})
	result, err := read.Execute(context.Background(), map[string]any{"path": "test.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "truncated") {
		t.Fatalf("expected truncation notice, got: %q", text[:100])
	}
	// Should not exceed maxOutputBytes + truncation notice length
	if len(text) > maxOutputBytes+200 {
		t.Fatalf("output too large: %d bytes", len(text))
	}
}

// --- headTailBuffer tests ---

func TestHeadTailBuffer_SmallInput(t *testing.T) {
	var b headTailBuffer
	b.headMax = 100
	b.tailMax = 100
	b.Write([]byte("hello world"))
	out := b.String()
	if out != "hello world" {
		t.Errorf("got %q", out)
	}
	if b.truncated {
		t.Error("should not be truncated")
	}
}

func TestHeadTailBuffer_ExactHead(t *testing.T) {
	var b headTailBuffer
	b.headMax = 5
	b.tailMax = 5
	b.Write([]byte("12345"))
	if b.truncated {
		t.Error("exactly headMax should not truncate")
	}
	if b.String() != "12345" {
		t.Errorf("got %q", b.String())
	}
}

func TestHeadTailBuffer_HeadPlusTail(t *testing.T) {
	var b headTailBuffer
	b.headMax = 10
	b.tailMax = 10

	// Write 30 bytes: head gets 10, tail gets last 10
	b.Write([]byte("HHHHHHHHHH"))          // fills head (10 bytes)
	b.Write([]byte("middle-data-ignored")) // 19 bytes to tail
	b.Write([]byte("TTTTTTTTTT"))          // last 10 to tail
	b.Close()
	defer func() {
		if b.SpillPath != "" {
			os.Remove(b.SpillPath)
		}
	}()

	out := b.String()
	if !strings.HasPrefix(out, "HHHHHHHHHH") {
		t.Error("should start with head content")
	}
	if !strings.HasSuffix(out, "TTTTTTTTTT") {
		t.Errorf("should end with tail content, got suffix: %q", out[len(out)-20:])
	}
	if !strings.Contains(out, "truncated") {
		t.Error("should contain truncation notice")
	}
}

func TestHeadTailBuffer_TailWraps(t *testing.T) {
	var b headTailBuffer
	b.headMax = 5
	b.tailMax = 5

	// Head: "AAAAA", then overflow: write 20 bytes, tail should keep last 5
	b.Write([]byte("AAAAA"))
	b.Write([]byte("12345678901234567890"))
	b.Close()
	defer func() {
		if b.SpillPath != "" {
			os.Remove(b.SpillPath)
		}
	}()

	out := b.String()
	if !strings.HasPrefix(out, "AAAAA") {
		t.Error("head should be preserved")
	}
	if !strings.HasSuffix(out, "67890") {
		t.Errorf("tail should have last 5 bytes, got: %q", out)
	}
}

func TestHeadTailBuffer_MultipleSmallWrites(t *testing.T) {
	var b headTailBuffer
	b.headMax = 3
	b.tailMax = 4

	for _, c := range "ABCDEFGHIJ" {
		b.Write([]byte(string(c)))
	}
	b.Close()
	defer func() {
		if b.SpillPath != "" {
			os.Remove(b.SpillPath)
		}
	}()

	out := b.String()
	if !strings.HasPrefix(out, "ABC") {
		t.Errorf("head should be ABC, got prefix: %q", out[:3])
	}
	if !strings.HasSuffix(out, "GHIJ") {
		t.Errorf("tail should be GHIJ, got: %q", out)
	}
}

func TestHeadTailBuffer_ZeroTail(t *testing.T) {
	var b headTailBuffer
	b.headMax = 5
	b.tailMax = 0

	b.Write([]byte("ABCDEFGHIJ"))
	b.Close()
	defer func() {
		if b.SpillPath != "" {
			os.Remove(b.SpillPath)
		}
	}()
	out := b.String()
	if !strings.HasPrefix(out, "ABCDE") {
		t.Error("head should be preserved")
	}
	if !strings.Contains(out, "truncated") {
		t.Error("should contain truncation notice")
	}
}

func TestHeadTailBuffer_SplitWrite(t *testing.T) {
	// Write that partially fills head and overflows to tail
	var b headTailBuffer
	b.headMax = 5
	b.tailMax = 5

	b.Write([]byte("ABCDEFGHIJ")) // 10 bytes in one write
	b.Close()
	defer func() {
		if b.SpillPath != "" {
			os.Remove(b.SpillPath)
		}
	}()
	out := b.String()
	if !strings.HasPrefix(out, "ABCDE") {
		t.Errorf("head: got %q", out)
	}
	if !strings.HasSuffix(out, "FGHIJ") {
		t.Errorf("tail: got %q", out)
	}
}

func TestHeadTailBuffer_SpillFile(t *testing.T) {
	var b headTailBuffer
	b.headMax = 10
	b.tailMax = 10

	// Write 50 bytes — should trigger spill
	data := "HHHHHHHHHH" + "MMMMMMMMMMMMMMMMMMMM" + "TTTTTTTTTT"
	b.Write([]byte(data))
	b.Close()

	if b.SpillPath == "" {
		t.Fatal("expected spill file to be created on truncation")
	}
	defer os.Remove(b.SpillPath)

	// Spill file should contain the complete output
	spillData, err := os.ReadFile(b.SpillPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(spillData) != data {
		t.Errorf("spill file content mismatch: got %d bytes, want %d", len(spillData), len(data))
	}

	// String() should reference the spill path
	out := b.String()
	if !strings.Contains(out, b.SpillPath) {
		t.Error("truncation notice should include spill file path")
	}
}

func TestHeadTailBuffer_NoSpillWhenNotTruncated(t *testing.T) {
	var b headTailBuffer
	b.headMax = 100
	b.tailMax = 100
	b.Write([]byte("small"))
	b.Close()
	if b.SpillPath != "" {
		os.Remove(b.SpillPath)
		t.Error("should not create spill file for small output")
	}
}

func TestHeadTailBuffer_AcceptedBytes(t *testing.T) {
	var b headTailBuffer
	b.headMax = 10
	b.tailMax = 10

	// First write fits entirely in head
	accepted, _ := b.Write([]byte("12345"))
	if accepted != 5 {
		t.Fatalf("expected 5 accepted, got %d", accepted)
	}

	// Second write partially fits (5 of 8 bytes)
	accepted, _ = b.Write([]byte("67890abc"))
	if accepted != 5 {
		t.Fatalf("expected 5 accepted (head had 5 remaining), got %d", accepted)
	}

	// Third write: head is full, nothing accepted
	accepted, _ = b.Write([]byte("more data"))
	if accepted != 0 {
		t.Fatalf("expected 0 accepted (head full), got %d", accepted)
	}
	b.Close()
}


