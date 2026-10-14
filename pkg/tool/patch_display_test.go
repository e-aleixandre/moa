package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func patchDisplayOf(t *testing.T, result core.Result) []PatchFileDisplay {
	t.Helper()
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	files, ok := result.Custom[core.CustomKeyPatchFiles].([]PatchFileDisplay)
	if !ok {
		t.Fatalf("no patch_files in Custom: %#v", result.Custom)
	}
	return files
}

func TestPatchDisplay_RecordsDiffsOutsideModelText(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("one\ntwo\nthree\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "old.go"), []byte("x\n"), 0o644)
	ft := NewFileTracker()
	ft.MarkRead(filepath.Join(dir, "a.go"))
	ft.MarkRead(filepath.Join(dir, "old.go"))
	tl := newPatchTool(t, dir, ft)

	result, _ := tl.Execute(nil, map[string]any{"patch": "*** Begin Patch\n*** Update File: a.go\n@@\n one\n-two\n+TWO\n+two-b\n*** Add File: n.txt\n+hello\n+\n*** Delete File: old.go\n*** End Patch"}, nil)
	files := patchDisplayOf(t, result)

	if got := resultText(result); got != "M a.go\nA n.txt\nD old.go" {
		t.Fatalf("model text changed: %q", got)
	}
	if len(files) != 3 {
		t.Fatalf("files = %d", len(files))
	}
	if f := files[0]; f.Op != "M" || f.Add != 2 || f.Del != 1 || !strings.Contains(f.Diff, "-two") || !strings.Contains(f.Diff, "+TWO") {
		t.Errorf("update = %+v", f)
	}
	if f := files[1]; f.Op != "A" || f.Add != 2 || f.Del != 0 || !strings.Contains(f.Diff, "+hello") {
		t.Errorf("add = %+v", f)
	}
	if f := files[2]; f.Op != "D" || f.Del != 1 || !strings.Contains(f.Diff, "-x") {
		t.Errorf("delete = %+v", f)
	}
}

// The patch said "keep" but tolerant matching kept the file's own indentation:
// the record must describe what was written, not what was proposed.
func TestPatchDisplay_ReflectsWhatWasWritten(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("\tkeep\nold\n"), 0o644)
	ft := NewFileTracker()
	ft.MarkRead(filepath.Join(dir, "a.txt"))

	result, _ := newPatchTool(t, dir, ft).Execute(nil, map[string]any{"patch": "*** Begin Patch\n*** Update File: a.txt\n@@\n keep\n-old\n+new\n*** End Patch"}, nil)
	files := patchDisplayOf(t, result)

	disk, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if !strings.Contains(files[0].Diff, "new") {
		t.Fatalf("diff = %q (disk %q)", files[0].Diff, disk)
	}
	// Whatever the backend did to the context line, the record must agree with disk.
	wantAdd, wantDel := 1, 1
	if string(disk) != "\tkeep\nnew\n" {
		wantAdd, wantDel = 2, 2
	}
	if files[0].Add != wantAdd || files[0].Del != wantDel {
		t.Errorf("counts +%d -%d, want +%d -%d (disk %q)", files[0].Add, files[0].Del, wantAdd, wantDel, disk)
	}
}

// A context line that merely reads "*** Move to: y" is not a rename.
func TestPatchDisplay_MoveToAsContextIsNotARename(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "x"), []byte("*** Move to: y\nz\n"), 0o644)
	ft := NewFileTracker()
	ft.MarkRead(filepath.Join(dir, "x"))

	result, _ := newPatchTool(t, dir, ft).Execute(nil, map[string]any{"patch": "*** Begin Patch\n*** Update File: x\n@@\n *** Move to: y\n-z\n+w\n*** End Patch"}, nil)
	files := patchDisplayOf(t, result)

	if files[0].Op != "M" || files[0].MovePath != "" {
		t.Errorf("file = %+v", files[0])
	}
}

func TestPatchDisplay_AddWithoutBodyAndBlankLines(t *testing.T) {
	dir := t.TempDir()
	result, _ := newPatchTool(t, dir, nil).Execute(nil, map[string]any{"patch": "*** Begin Patch\n*** Add File: e.txt\n*** Add File: b.txt\n+a\n   \n+b\n*** End Patch"}, nil)
	files := patchDisplayOf(t, result)

	disk, _ := os.ReadFile(filepath.Join(dir, "b.txt"))
	if files[1].Add != strings.Count(string(disk), "\n") {
		t.Errorf("b.txt counts +%d, disk %q", files[1].Add, disk)
	}
	empty, _ := os.ReadFile(filepath.Join(dir, "e.txt"))
	if string(empty) != "\n" || files[0].Add != 1 {
		t.Errorf("e.txt disk %q add %d", empty, files[0].Add)
	}
}

func TestPatchDisplay_BoundsDiffTextKeepsCounts(t *testing.T) {
	dir := t.TempDir()
	var body strings.Builder
	for i := 0; i < 3000; i++ {
		body.WriteString("+line of generated content number\n")
	}
	patch := "*** Begin Patch\n*** Add File: big.txt\n" + body.String() + "*** Add File: after.txt\n+x\n*** End Patch"
	result, _ := newPatchTool(t, dir, nil).Execute(nil, map[string]any{"patch": patch}, nil)
	files := patchDisplayOf(t, result)

	total := 0
	for _, f := range files {
		total += len(f.Diff)
	}
	if total > patchDisplayMaxBytes {
		t.Errorf("diff bytes %d over cap", total)
	}
	if files[0].Add != 3000 || !files[0].Truncated {
		t.Errorf("big = add %d truncated %v", files[0].Add, files[0].Truncated)
	}
	if files[1].Add != 1 || !files[1].Truncated || files[1].Diff != "" {
		t.Errorf("after = %+v", files[1])
	}
}

func TestPatchDisplay_RenameCarriesDestination(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a"), []byte("1\n"), 0o644)
	ft := NewFileTracker()
	ft.MarkRead(filepath.Join(dir, "a"))
	result, _ := newPatchTool(t, dir, ft).Execute(nil, map[string]any{"patch": "*** Begin Patch\n*** Update File: a\n*** Move to: sub/b\n@@\n-1\n+2\n*** End Patch"}, nil)
	f := patchDisplayOf(t, result)[0]
	if f.Op != "R" || f.MovePath != "sub/b" || f.Add != 1 {
		t.Errorf("file = %+v", f)
	}
}

func TestPatchDisplay_DeleteCountsAndShowsRemovedLines(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "old.go"), []byte("a\nb\nc\n"), 0o644)
	ft := NewFileTracker()
	ft.MarkRead(filepath.Join(dir, "old.go"))
	result, _ := newPatchTool(t, dir, ft).Execute(nil, map[string]any{"patch": "*** Begin Patch\n*** Delete File: old.go\n*** End Patch"}, nil)
	f := patchDisplayOf(t, result)[0]
	if f.Op != "D" || f.Del != 3 || f.Add != 0 || !strings.Contains(f.Diff, "-b") {
		t.Errorf("delete = %+v", f)
	}
}

// A move onto an existing file overwrites it: the card must show what was lost,
// not +0 −0 because the source was only compared with itself.
func TestPatchDisplay_MoveOntoExistingFileShowsWhatWasOverwritten(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a"), []byte("new\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b"), []byte("precious\nlines\n"), 0o644)
	ft := NewFileTracker()
	ft.MarkRead(filepath.Join(dir, "a"))
	result, _ := newPatchTool(t, dir, ft).Execute(nil, map[string]any{"patch": "*** Begin Patch\n*** Update File: a\n*** Move to: b\n@@\n new\n*** End Patch"}, nil)
	f := patchDisplayOf(t, result)[0]
	if f.Op != "R" || f.Del != 2 || f.Add != 1 || !strings.Contains(f.Diff, "-precious") {
		t.Errorf("move = %+v", f)
	}
}

// A move to a free destination, with edits, still diffs source against result.
func TestPatchDisplay_MoveToFreeDestinationDiffsAgainstTheSource(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a"), []byte("1\n2\n"), 0o644)
	ft := NewFileTracker()
	ft.MarkRead(filepath.Join(dir, "a"))
	result, _ := newPatchTool(t, dir, ft).Execute(nil, map[string]any{"patch": "*** Begin Patch\n*** Update File: a\n*** Move to: c\n@@\n 1\n-2\n+3\n*** End Patch"}, nil)
	f := patchDisplayOf(t, result)[0]
	if f.Add != 1 || f.Del != 1 {
		t.Errorf("move = %+v", f)
	}
}

func chainedFixture(t *testing.T, files map[string]string) (string, *FileTracker) {
	t.Helper()
	dir := t.TempDir()
	ft := NewFileTracker()
	for name, body := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
		ft.MarkRead(filepath.Join(dir, name))
	}
	return dir, ft
}

// Each operation's "before" is the patch's own working state: what an earlier
// operation of the same patch last wrote to that path, else the disk.
func TestPatchDisplay_MoveThenDeleteShowsWhatTheMoveWrote(t *testing.T) {
	dir, ft := chainedFixture(t, map[string]string{"x": "source-x\n", "y": "original-y\n"})
	result, _ := newPatchTool(t, dir, ft).Execute(nil, map[string]any{"patch": "*** Begin Patch\n*** Update File: x\n*** Move to: y\n@@\n source-x\n*** Delete File: y\n*** End Patch"}, nil)
	files := patchDisplayOf(t, result)
	if !strings.Contains(files[1].Diff, "-source-x") || strings.Contains(files[1].Diff, "original-y") {
		t.Errorf("delete after move = %+v", files[1])
	}
}

func TestPatchDisplay_DeleteThenMoveOntoItDiffsAgainstNothing(t *testing.T) {
	dir, ft := chainedFixture(t, map[string]string{"x": "source-x\n", "y": "original-y\n"})
	result, _ := newPatchTool(t, dir, ft).Execute(nil, map[string]any{"patch": "*** Begin Patch\n*** Delete File: y\n*** Update File: x\n*** Move to: y\n@@\n source-x\n*** End Patch"}, nil)
	files := patchDisplayOf(t, result)
	if strings.Contains(files[1].Diff, "original-y") || files[1].Del != 0 || files[1].Add != 1 {
		t.Errorf("move after delete = %+v", files[1])
	}
}

func TestPatchDisplay_MoveThenMoveTakesTheFirstMovesResultAsBefore(t *testing.T) {
	dir, ft := chainedFixture(t, map[string]string{"x": "source-x\n", "y": "original-y\n"})
	result, _ := newPatchTool(t, dir, ft).Execute(nil, map[string]any{"patch": "*** Begin Patch\n*** Update File: x\n*** Move to: y\n@@\n source-x\n*** Update File: y\n*** Move to: z\n@@\n-original-y\n+next\n*** End Patch"}, nil)
	files := patchDisplayOf(t, result)
	// z is free, so the second move diffs against what the first wrote to y.
	if !strings.Contains(files[1].Diff, "-source-x") || strings.Contains(files[1].Diff, "original-y") {
		t.Errorf("move after move = %+v", files[1])
	}
}

func TestSummarizePatchFiles_KeepsTheRealTotalsPastTheCap(t *testing.T) {
	var all []PatchFileDisplay
	for i := 0; i < 101; i++ {
		all = append(all, PatchFileDisplay{Path: "f", Op: "M", Add: 1, Del: 2, Diff: "@@ -1 +1 @@"})
	}
	files, more := SummarizePatchFiles(all)
	if len(files) != 100 || more == nil || more.Files != 1 || more.Add != 1 || more.Del != 2 {
		t.Errorf("files=%d more=%+v", len(files), more)
	}
	if _, none := SummarizePatchFiles(all[:3]); none != nil {
		t.Errorf("no cap, yet more = %+v", none)
	}
}
