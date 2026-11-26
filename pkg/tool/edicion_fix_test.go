package tool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func runTool(t *testing.T, tl core.Tool, args map[string]any) core.Result {
	t.Helper()
	res, err := tl.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestPatch_BlankContextLine(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.go")
	mustWrite(t, f, "a\n\nb\nc\n")
	ft := NewFileTracker()
	ft.MarkRead(f)
	res := runTool(t, newPatchTool(t, dir, ft), map[string]any{"patch": "*** Begin Patch\n*** Update File: a.go\n@@\n a\n\n-b\n+B\n*** End Patch"})
	if res.IsError {
		t.Fatal(resultText(res))
	}
	got, _ := os.ReadFile(f)
	if string(got) != "a\n\nB\nc\n" {
		t.Fatalf("got %q", got)
	}
}

func TestPatch_TrailingBlankLinesTrimmed(t *testing.T) {
	chunks, _, err := parseChunks(strings.Split("@@\n-a\n+b\n\n\n", "\n"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(chunks[0].Ops); n != 2 {
		t.Fatalf("want 2 ops, got %d: %+v", n, chunks[0].Ops)
	}
}

func TestPatch_MissingAnchorIsError(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.go")
	orig := "func first() {\n\tx()\n}\n"
	mustWrite(t, f, orig)
	ft := NewFileTracker()
	ft.MarkRead(f)
	res := runTool(t, newPatchTool(t, dir, ft), map[string]any{"patch": "*** Begin Patch\n*** Update File: a.go\n@@ func sekond()\n-\tx()\n+\ty()\n*** End Patch"})
	if !res.IsError || !strings.Contains(resultText(res), "anchor") {
		t.Fatalf("want anchor error, got %q", resultText(res))
	}
	got, _ := os.ReadFile(f)
	if string(got) != orig {
		t.Fatalf("file modified: %q", got)
	}
}

func TestPatch_PureInsertionBelowAnchor(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.go")
	mustWrite(t, f, "import (\n\t\"os\"\n)\n")
	ft := NewFileTracker()
	ft.MarkRead(f)
	res := runTool(t, newPatchTool(t, dir, ft), map[string]any{"patch": "*** Begin Patch\n*** Update File: a.go\n@@ import (\n+\t\"fmt\"\n*** End Patch"})
	if res.IsError {
		t.Fatal(resultText(res))
	}
	got, _ := os.ReadFile(f)
	if string(got) != "import (\n\t\"fmt\"\n\t\"os\"\n)\n" {
		t.Fatalf("got %q", got)
	}
}

func TestFileTracker_MarkedAfterWrite(t *testing.T) {
	dir := t.TempDir()
	cfg := ToolConfig{WorkspaceRoot: dir, FileTracker: NewFileTracker(), DisableSandbox: true}
	res := runTool(t, NewWrite(cfg), map[string]any{"path": "n.txt", "content": "hello\n"})
	if res.IsError {
		t.Fatal(resultText(res))
	}
	res = runTool(t, NewEdit(cfg), map[string]any{"path": "n.txt", "oldText": "hello", "newText": "bye"})
	if res.IsError {
		t.Fatalf("edit after write: %s", resultText(res))
	}
	res = runTool(t, NewWrite(cfg), map[string]any{"path": "n.txt", "content": "again\n"})
	if res.IsError {
		t.Fatalf("overwrite after write: %s", resultText(res))
	}
}

func TestFileTracker_MarkedAfterPatch(t *testing.T) {
	dir := t.TempDir()
	ft := NewFileTracker()
	pt := newPatchTool(t, dir, ft)
	patch := "*** Begin Patch\n*** Add File: n.txt\n+one\n*** End Patch"
	if res := runTool(t, pt, map[string]any{"patch": patch}); res.IsError {
		t.Fatal(resultText(res))
	}
	cfg := ToolConfig{WorkspaceRoot: dir, FileTracker: ft, DisableSandbox: true}
	if res := runTool(t, NewEdit(cfg), map[string]any{"path": "n.txt", "oldText": "one", "newText": "two"}); res.IsError {
		t.Fatalf("edit after Add: %s", resultText(res))
	}
	// Update then Move: target must be marked.
	patch = "*** Begin Patch\n*** Update File: n.txt\n*** Move to: m.txt\n@@\n-two\n+three\n*** End Patch"
	if res := runTool(t, pt, map[string]any{"patch": patch}); res.IsError {
		t.Fatal(resultText(res))
	}
	if res := runTool(t, NewEdit(cfg), map[string]any{"path": "m.txt", "oldText": "three", "newText": "four"}); res.IsError {
		t.Fatalf("edit after Move: %s", resultText(res))
	}
	patch = "*** Begin Patch\n*** Update File: m.txt\n@@\n-four\n+five\n*** End Patch"
	if res := runTool(t, pt, map[string]any{"patch": patch}); res.IsError {
		t.Fatalf("update after update: %s", resultText(res))
	}
}

func TestFind_SlashPattern(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "pkg", "sub"))
	mustWrite(t, filepath.Join(dir, "pkg", "sub", "x.go"), "")
	mustWrite(t, filepath.Join(dir, "pkg", "y.go"), "")
	mustWrite(t, filepath.Join(dir, "z.go"), "")
	cfg := ToolConfig{WorkspaceRoot: dir, DisableSandbox: true}
	check := func(label string) {
		res := runTool(t, NewFind(cfg), map[string]any{"pattern": "pkg/**/*.go"})
		out := resultText(res)
		if !strings.Contains(out, "x.go") || !strings.Contains(out, "y.go") || strings.Contains(out, "z.go") {
			t.Fatalf("%s: got %q", label, out)
		}
	}
	check("default")

	// Fallback: a PATH holding only `find`, so neither fd nor fdfind resolves.
	bin := t.TempDir()
	findBin, err := exec.LookPath("find")
	if err != nil {
		t.Skip("find not available")
	}
	if err := os.Symlink(findBin, filepath.Join(bin, "find")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	check("fallback")
}
