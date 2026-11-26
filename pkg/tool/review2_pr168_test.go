package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func cfgFT(dir string, ft *FileTracker) ToolConfig {
	return ToolConfig{WorkspaceRoot: dir, FileTracker: ft, DisableSandbox: true}
}

// The file the agent saw was deleted outside moa. `read` fails on a missing
// file and never refreshes the stamp, so `write` is refused for the rest of
// the session: nothing can be clobbered, yet the agent cannot recreate it.
func TestReview168b_WriteAfterExternalDeleteDeadlocks(t *testing.T) {
	dir := t.TempDir()
	cfg := cfgFT(dir, NewFileTracker())
	if res := runTool(t, NewWrite(cfg), map[string]any{"path": "n.txt", "content": "v1\n"}); res.IsError {
		t.Fatal(resultText(res))
	}
	if err := os.Remove(filepath.Join(dir, "n.txt")); err != nil {
		t.Fatal(err)
	}
	if res := runTool(t, NewRead(cfg), map[string]any{"path": "n.txt"}); !res.IsError {
		t.Fatal("read of a missing file should fail")
	}
	if res := runTool(t, NewWrite(cfg), map[string]any{"path": "n.txt", "content": "v2\n"}); res.IsError {
		t.Fatalf("cannot recreate a deleted file: %s", resultText(res))
	}
}

// Same deadlock through moa's own tools: apply_patch Delete leaves the old
// stamp behind, so a later write of that path is refused forever.
func TestReview168b_WriteAfterOwnPatchDelete(t *testing.T) {
	dir := t.TempDir()
	ft := NewFileTracker()
	f := filepath.Join(dir, "n.txt")
	mustWrite(t, f, "old\n")
	ft.MarkRead(f)
	if res := runTool(t, newPatchTool(t, dir, ft), map[string]any{"patch": "*** Begin Patch\n*** Delete File: n.txt\n*** End Patch"}); res.IsError {
		t.Fatal(resultText(res))
	}
	if res := runTool(t, NewWrite(cfgFT(dir, ft)), map[string]any{"path": "n.txt", "content": "new\n"}); res.IsError {
		t.Fatalf("write after own Delete: %s", resultText(res))
	}
}

// And after a Move: the source path keeps a stale stamp.
func TestReview168b_WriteOldPathAfterOwnMove(t *testing.T) {
	dir := t.TempDir()
	ft := NewFileTracker()
	f := filepath.Join(dir, "n.txt")
	mustWrite(t, f, "a\n")
	ft.MarkRead(f)
	if res := runTool(t, newPatchTool(t, dir, ft), map[string]any{"patch": "*** Begin Patch\n*** Update File: n.txt\n*** Move to: m.txt\n@@\n-a\n+b\n*** End Patch"}); res.IsError {
		t.Fatal(resultText(res))
	}
	if res := runTool(t, NewWrite(cfgFT(dir, ft)), map[string]any{"path": "n.txt", "content": "shim\n"}); res.IsError {
		t.Fatalf("write old path after own Move: %s", resultText(res))
	}
}

// find: a relative `path` parameter and a root with a space.
func TestReview168b_FindRelativePathParamAndSpaces(t *testing.T) {
	root := filepath.Join(t.TempDir(), "my proj")
	mustMkdir(t, filepath.Join(root, "web", "src", "ui"))
	mustWrite(t, filepath.Join(root, "web", "src", "ui", "a.ts"), "")
	mustWrite(t, filepath.Join(root, "web", "src", "b.ts"), "")
	cfg := ToolConfig{WorkspaceRoot: root, DisableSandbox: true}
	for mode, out := range findBothCfgPath(t, cfg, "src/**/*.ts", "web") {
		if !strings.Contains(out, "a.ts") || !strings.Contains(out, "b.ts") {
			t.Errorf("%s: %q", mode, out)
		}
	}
}

func findBothCfgPath(t *testing.T, cfg ToolConfig, pattern, path string) map[string]string {
	t.Helper()
	args := map[string]any{"pattern": pattern}
	if path != "" {
		args["path"] = path
	}
	out := map[string]string{"fd": resultText(runTool(t, NewFind(cfg), args))}
	bin := t.TempDir()
	findBin, err := exec.LookPath("find")
	if err != nil {
		t.Skip("find not available")
	}
	if err := os.Symlink(findBin, filepath.Join(bin, "find")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	out["find"] = resultText(runTool(t, NewFind(cfg), args))
	return out
}

// An external change must not block edit, multiedit or Update: they match
// their old text against the disk. They apply, say so, and refresh the mark.
func TestReview168b_StaleFileEditsApplyWithNote(t *testing.T) {
	cases := map[string]func(dir string, ft *FileTracker) core.Result{
		"edit": func(dir string, ft *FileTracker) core.Result {
			return runTool(t, NewEdit(cfgFT(dir, ft)), map[string]any{"path": "n.txt", "oldText": "target", "newText": "done"})
		},
		"multiedit": func(dir string, ft *FileTracker) core.Result {
			return runTool(t, NewMultiEdit(cfgFT(dir, ft)), map[string]any{"path": "n.txt", "edits": []any{map[string]any{"oldText": "target", "newText": "done"}}})
		},
		"patch": func(dir string, ft *FileTracker) core.Result {
			return runTool(t, newPatchTool(t, dir, ft), map[string]any{"patch": "*** Begin Patch\n*** Update File: n.txt\n@@\n-target\n+done\n*** End Patch"})
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			f := filepath.Join(dir, "n.txt")
			mustWrite(t, f, "keep\ntarget\n")
			ft := NewFileTracker()
			ft.MarkRead(f)
			mustWrite(t, f, "keep\ntarget\nadded elsewhere\n")
			res := run(dir, ft)
			if res.IsError {
				t.Fatalf("refused: %s", resultText(res))
			}
			if !strings.Contains(resultText(res), "changed outside this session") || !strings.HasSuffix(strings.TrimSpace(strings.SplitN(resultText(res), "\n\n", 2)[0]), "Read it again before rewriting it whole.") {
				t.Fatalf("no note: %q", resultText(res))
			}
			got, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "keep\ndone\nadded elsewhere\n" {
				t.Fatalf("got %q", got)
			}
			if ft.Changed(f) {
				t.Fatal("mark not refreshed")
			}
		})
	}
}
