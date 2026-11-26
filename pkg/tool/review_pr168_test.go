package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// --- apply_patch anchors -------------------------------------------------

func applyOn(t *testing.T, orig, patch string) (string, string, bool) {
	t.Helper()
	dir := t.TempDir()
	f := filepath.Join(dir, "a.go")
	mustWrite(t, f, orig)
	ft := NewFileTracker()
	ft.MarkRead(f)
	res := runTool(t, newPatchTool(t, dir, ft), map[string]any{"patch": patch})
	got, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(got), resultText(res), res.IsError
}

// "@@ func main" is a prefix of "func mainHelper()": the substring fallback
// lands on the helper and edits it. Codex would reject the anchor (no whole
// line equals it); a boundary-aware fallback would pick main().
func TestReview168_AnchorSubstringPicksPrefixSibling(t *testing.T) {
	orig := "func mainHelper() error {\n\treturn nil\n}\n\nfunc main() error {\n\treturn nil\n}\n"
	got, msg, isErr := applyOn(t, orig, "*** Begin Patch\n*** Update File: a.go\n@@ func main\n-\treturn nil\n+\treturn run()\n*** End Patch")
	if strings.HasPrefix(got, "func mainHelper() error {\n\treturn run()") {
		t.Fatalf("edited mainHelper instead of main (isErr=%v msg=%q):\n%s", isErr, msg, got)
	}
}

// The anchor text appears in a comment before the real definition.
func TestReview168_AnchorSubstringPicksComment(t *testing.T) {
	orig := "func setup() {\n\t// keep in sync with func run()\n\tx := 1\n\t_ = x\n}\n\nfunc run() {\n\tx := 1\n\t_ = x\n}\n"
	got, msg, isErr := applyOn(t, orig, "*** Begin Patch\n*** Update File: a.go\n@@ func run()\n-\tx := 1\n+\tx := 2\n*** End Patch")
	if strings.Contains(got, "func run()\n\tx := 2") || strings.Index(got, "x := 2") < strings.Index(got, "func run() {") {
		t.Fatalf("edited setup() instead of run() (isErr=%v msg=%q):\n%s", isErr, msg, got)
	}
}

// Two lines contain the anchor and neither equals it: the fallback must not
// pick one silently.
func TestReview168_AnchorSubstringAmbiguousIsError(t *testing.T) {
	orig := "func (a *A) Close() error {\n\treturn nil\n}\n\nfunc (b *B) Close() error {\n\treturn nil\n}\n"
	got, msg, isErr := applyOn(t, orig, "*** Begin Patch\n*** Update File: a.go\n@@ Close() error\n-\treturn nil\n+\treturn errClosed\n*** End Patch")
	if !isErr || got != orig {
		t.Fatalf("ambiguous anchor applied silently (isErr=%v msg=%q):\n%s", isErr, msg, got)
	}
}

// Guard: the useful case the fallback was added for must keep working.
func TestReview168_AnchorSignaturePrefixStillWorks(t *testing.T) {
	orig := "func helper() {\n\treturn\n}\n\nfunc main() {\n\treturn\n}\n"
	got, msg, isErr := applyOn(t, orig, "*** Begin Patch\n*** Update File: a.go\n@@ func main()\n-\treturn\n+\tos.Exit(0)\n*** End Patch")
	want := "func helper() {\n\treturn\n}\n\nfunc main() {\n\tos.Exit(0)\n}\n"
	if isErr || got != want {
		t.Fatalf("isErr=%v msg=%q got:\n%s", isErr, msg, got)
	}
}

// --- apply_patch blank lines / whitespace / CRLF -------------------------

// A trailing " " context line the model wrote on purpose to pick the second
// occurrence is dropped unconditionally, so the first occurrence is edited.
// Codex tries the full pattern first and only then retries without it.
func TestReview168_TrailingBlankContextDisambiguates(t *testing.T) {
	orig := "x\ny\nz\nx\ny\n\nw\n"
	got, msg, isErr := applyOn(t, orig, "*** Begin Patch\n*** Update File: a.go\n@@\n x\n-y\n+Y\n \n*** End Patch")
	want := "x\ny\nz\nx\nY\n\nw\n"
	if isErr || got != want {
		t.Fatalf("isErr=%v msg=%q got %q want %q", isErr, msg, got, want)
	}
}

// Guards (expected GREEN): anchor with trailing whitespace on either side,
// blank context against a CRLF file.
func TestReview168_AnchorTrailingWhitespace(t *testing.T) {
	orig := "func a() {\n\treturn\n}\nfunc b() {  \n\treturn\n}\n"
	got, msg, isErr := applyOn(t, orig, "*** Begin Patch\n*** Update File: a.go\n@@ func b() {\t \n-\treturn\n+\tpanic(1)\n*** End Patch")
	if isErr || !strings.HasSuffix(got, "func b() {  \n\tpanic(1)\n}\n") {
		t.Fatalf("isErr=%v msg=%q got %q", isErr, msg, got)
	}
}

func TestReview168_CRLFFileBlankContextAndAnchor(t *testing.T) {
	orig := "func a() {\r\n\tx()\r\n}\r\n\r\nfunc b() {\r\n\tx()\r\n\r\n\ty()\r\n}\r\n"
	got, msg, isErr := applyOn(t, orig, "*** Begin Patch\n*** Update File: a.go\n@@ func b() {\n \tx()\n\n-\ty()\n+\tz()\n*** End Patch")
	// Placement only: the patch's context lines replace the file's, so their
	// \r is lost (mixed endings, pre-existing, Codex keeps the originals).
	if isErr || !strings.Contains(strings.ReplaceAll(got, "\r", ""), "func b() {\n\tx()\n\n\tz()") {
		t.Fatalf("isErr=%v msg=%q got %q", isErr, msg, got)
	}
}

// --- FileTracker after writes --------------------------------------------

// After the agent writes a file, someone else changes it; a blind overwrite
// must still be refused (before the PR it was, by accident). MarkRead stores
// a timestamp that nothing compares.
func TestReview168_WriteAfterExternalChangeRefused(t *testing.T) {
	dir := t.TempDir()
	cfg := ToolConfig{WorkspaceRoot: dir, FileTracker: NewFileTracker(), DisableSandbox: true}
	if res := runTool(t, NewWrite(cfg), map[string]any{"path": "n.txt", "content": "mine\n"}); res.IsError {
		t.Fatal(resultText(res))
	}
	f := filepath.Join(dir, "n.txt")
	mustWrite(t, f, "changed outside the agent, longer\n")
	res := runTool(t, NewWrite(cfg), map[string]any{"path": "n.txt", "content": "clobber\n"})
	got, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("external change overwritten blindly; file now %q", got)
	}
}

// Same for apply_patch Delete after an own Add.
func TestReview168_DeleteAfterExternalChangeRefused(t *testing.T) {
	dir := t.TempDir()
	ft := NewFileTracker()
	pt := newPatchTool(t, dir, ft)
	if res := runTool(t, pt, map[string]any{"patch": "*** Begin Patch\n*** Add File: n.txt\n+one\n*** End Patch"}); res.IsError {
		t.Fatal(resultText(res))
	}
	f := filepath.Join(dir, "n.txt")
	mustWrite(t, f, "edited by the user, longer\n")
	res := runTool(t, pt, map[string]any{"patch": "*** Begin Patch\n*** Delete File: n.txt\n*** End Patch"})
	if !res.IsError {
		t.Fatal("deleted a file changed externally after our own Add")
	}
}

// Guard (expected GREEN): edit after an external change that touches the
// old text fails because edit matches against disk.
func TestReview168_EditAfterExternalChangeFails(t *testing.T) {
	dir := t.TempDir()
	cfg := ToolConfig{WorkspaceRoot: dir, FileTracker: NewFileTracker(), DisableSandbox: true}
	runTool(t, NewWrite(cfg), map[string]any{"path": "n.txt", "content": "hello\n"})
	mustWrite(t, filepath.Join(dir, "n.txt"), "hola\n")
	if res := runTool(t, NewEdit(cfg), map[string]any{"path": "n.txt", "oldText": "hello", "newText": "bye"}); !res.IsError {
		t.Fatal("edit applied over an external change")
	}
}

// Multiedit marks too (the PR's own test never calls multiedit).
func TestReview168_MultieditMarks(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "m.txt")
	mustWrite(t, f, "a\n")
	ft := NewFileTracker()
	ft.MarkRead(f)
	cfg := ToolConfig{WorkspaceRoot: dir, FileTracker: ft, DisableSandbox: true}
	res := runTool(t, NewMultiEdit(cfg), map[string]any{"path": "m.txt", "edits": []any{map[string]any{"oldText": "a", "newText": "b"}}})
	if res.IsError {
		t.Fatal(resultText(res))
	}
	if !ft.WasRead(f) {
		t.Fatal("not marked")
	}
}

// --- find ------------------------------------------------------------------

func findBoth(t *testing.T, root, pattern string) map[string]string {
	t.Helper()
	cfg := ToolConfig{WorkspaceRoot: root, DisableSandbox: true}
	out := map[string]string{}
	if _, err := exec.LookPath("fd"); err == nil {
		out["fd"] = resultText(runTool(t, NewFind(cfg), map[string]any{"pattern": pattern}))
	} else if _, err := exec.LookPath("fdfind"); err == nil {
		out["fd"] = resultText(runTool(t, NewFind(cfg), map[string]any{"pattern": pattern}))
	}
	bin := t.TempDir()
	findBin, err := exec.LookPath("find")
	if err != nil {
		t.Skip("find not available")
	}
	if err := os.Symlink(findBin, filepath.Join(bin, "find")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	out["find"] = resultText(runTool(t, NewFind(cfg), map[string]any{"pattern": pattern}))
	return out
}

// The workspace lives under a directory called "src": "src/**/*.go" must not
// match every file just because an ancestor outside the workspace is "src".
func TestReview168_FindSlashMatchesAncestorOutsideWorkspace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "src", "proj")
	mustMkdir(t, filepath.Join(root, "lib"))
	mustWrite(t, filepath.Join(root, "a.go"), "")
	mustWrite(t, filepath.Join(root, "lib", "b.go"), "")
	for mode, out := range findBoth(t, root, "src/**/*.go") {
		if strings.Contains(out, ".go") {
			t.Errorf("%s: matched through an ancestor outside the workspace:\n%s", mode, out)
		}
	}
}

func setupPkgTree(t *testing.T) string {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, "pkg", "sub"))
	mustWrite(t, filepath.Join(dir, "pkg", "sub", "x.go"), "")
	mustWrite(t, filepath.Join(dir, "pkg", "y.go"), "")
	mustWrite(t, filepath.Join(dir, "z.go"), "")
	return dir
}

// Leading "**/" in the fallback must match top-level files too.
func TestReview168_FindLeadingDoubleStar(t *testing.T) {
	for mode, out := range findBoth(t, setupPkgTree(t), "**/z.go") {
		if !strings.Contains(out, "z.go") {
			t.Errorf("%s: got %q", mode, out)
		}
	}
}

// A path-glob with a regex-ish parent: the search root contains glob
// metacharacters and must be matched literally.
func TestReview168_FindRootWithGlobMetacharacters(t *testing.T) {
	root := filepath.Join(t.TempDir(), "w[1]", "src")
	mustMkdir(t, filepath.Join(root, "lib"))
	mustWrite(t, filepath.Join(root, "lib", "b.go"), "")
	for mode, out := range findBoth(t, root, "lib/*.go") {
		if !strings.Contains(out, "b.go") {
			t.Errorf("%s: got %q", mode, out)
		}
	}
}

// Multiedit on a file the agent never read must mark it afterwards. The file
// is changed first through a fresh tracker so the mark can only come from the
// tool.
func TestFileTracker_MultieditMarksUnreadFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "m.txt")
	mustWrite(t, f, "a\n")
	ft := NewFileTracker()
	cfg := ToolConfig{WorkspaceRoot: dir, FileTracker: ft, DisableSandbox: true}
	runTool(t, NewMultiEdit(cfg), map[string]any{"path": "m.txt", "edits": []any{map[string]any{"oldText": "a", "newText": "b"}}})
	if ft.WasRead(f) {
		t.Fatal("multiedit must refuse an unread file, not mark it")
	}
	ft.MarkRead(f)
	res := runTool(t, NewMultiEdit(cfg), map[string]any{"path": "m.txt", "edits": []any{map[string]any{"oldText": "a", "newText": "b"}}})
	if res.IsError {
		t.Fatal(resultText(res))
	}
	// Second multiedit only works if the first one re-stamped the file.
	res = runTool(t, NewMultiEdit(cfg), map[string]any{"path": "m.txt", "edits": []any{map[string]any{"oldText": "b", "newText": "c"}}})
	if res.IsError {
		t.Fatalf("second multiedit refused: %s", resultText(res))
	}
}
