package tool

import (
	"strings"
	"testing"
)

func TestParsePatch_AddFile(t *testing.T) {
	patch := `*** Begin Patch
*** Add File: hello.txt
+Hello world
+Line two
*** End Patch`
	hunks, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(hunks))
	}
	h := hunks[0]
	if h.Type != HunkAdd {
		t.Errorf("expected HunkAdd, got %d", h.Type)
	}
	if h.Path != "hello.txt" {
		t.Errorf("expected hello.txt, got %s", h.Path)
	}
	if !strings.Contains(h.Content, "Hello world") || !strings.Contains(h.Content, "Line two") {
		t.Errorf("content mismatch: %q", h.Content)
	}
}

func TestParsePatch_DeleteFile(t *testing.T) {
	patch := `*** Begin Patch
*** Delete File: obsolete.txt
*** End Patch`
	hunks, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(hunks))
	}
	if hunks[0].Type != HunkDelete {
		t.Errorf("expected HunkDelete")
	}
	if hunks[0].Path != "obsolete.txt" {
		t.Errorf("expected obsolete.txt, got %s", hunks[0].Path)
	}
}

func TestParsePatch_UpdateOneChunk(t *testing.T) {
	patch := `*** Begin Patch
*** Update File: src/main.go
@@ func greet()
-	fmt.Println("Hi")
+	fmt.Println("Hello, world!")
*** End Patch`
	hunks, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(hunks))
	}
	h := hunks[0]
	if h.Type != HunkUpdate {
		t.Errorf("expected HunkUpdate")
	}
	if len(h.Chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(h.Chunks))
	}
	c := h.Chunks[0]
	if c.Context != "func greet()" {
		t.Errorf("expected context 'func greet()', got %q", c.Context)
	}
	if len(c.Ops) != 2 {
		t.Fatalf("expected 2 ops, got %d", len(c.Ops))
	}
	if c.Ops[0].Type != OpRemove || c.Ops[0].Line != "\tfmt.Println(\"Hi\")" {
		t.Errorf("op 0 mismatch: %+v", c.Ops[0])
	}
	if c.Ops[1].Type != OpAdd || c.Ops[1].Line != "\tfmt.Println(\"Hello, world!\")" {
		t.Errorf("op 1 mismatch: %+v", c.Ops[1])
	}
}

func TestParsePatch_UpdateMultipleChunks(t *testing.T) {
	patch := `*** Begin Patch
*** Update File: app.go
@@ func foo()
-old1
+new1
@@ func bar()
-old2
+new2
*** End Patch`
	hunks, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks[0].Chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(hunks[0].Chunks))
	}
	if hunks[0].Chunks[0].Context != "func foo()" {
		t.Errorf("chunk 0 context: %q", hunks[0].Chunks[0].Context)
	}
	if hunks[0].Chunks[1].Context != "func bar()" {
		t.Errorf("chunk 1 context: %q", hunks[0].Chunks[1].Context)
	}
}

func TestParsePatch_UpdateWithMove(t *testing.T) {
	patch := `*** Begin Patch
*** Update File: old/path.go
*** Move to: new/path.go
@@ func main()
-old
+new
*** End Patch`
	hunks, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if hunks[0].MovePath != "new/path.go" {
		t.Errorf("expected MovePath new/path.go, got %q", hunks[0].MovePath)
	}
}

func TestParsePatch_ContextAnchor(t *testing.T) {
	patch := `*** Begin Patch
*** Update File: test.go
@@ import (
 	"fmt"
-	"os"
+	"io"
*** End Patch`
	hunks, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	c := hunks[0].Chunks[0]
	if c.Context != "import (" {
		t.Errorf("expected context 'import (', got %q", c.Context)
	}
	// Should have 3 ops: context, remove, add
	if len(c.Ops) != 3 {
		t.Fatalf("expected 3 ops, got %d", len(c.Ops))
	}
	if c.Ops[0].Type != OpContext {
		t.Errorf("op 0 should be context, got %d", c.Ops[0].Type)
	}
}

func TestParsePatch_EndOfFileMarker(t *testing.T) {
	patch := `*** Begin Patch
*** Update File: test.go
@@ func main()
-old
+new
*** End of File
*** End Patch`
	hunks, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 || len(hunks[0].Chunks) != 1 {
		t.Errorf("expected 1 hunk with 1 chunk")
	}
}

func TestParsePatch_MultiFile(t *testing.T) {
	patch := `*** Begin Patch
*** Add File: new.txt
+content
*** Update File: existing.go
@@ func main()
-old
+new
*** Delete File: gone.txt
*** End Patch`
	hunks, err := ParsePatch(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 3 {
		t.Fatalf("expected 3 hunks, got %d", len(hunks))
	}
	if hunks[0].Type != HunkAdd {
		t.Errorf("hunk 0: expected HunkAdd")
	}
	if hunks[1].Type != HunkUpdate {
		t.Errorf("hunk 1: expected HunkUpdate")
	}
	if hunks[2].Type != HunkDelete {
		t.Errorf("hunk 2: expected HunkDelete")
	}
}

func TestParsePatch_MissingBegin(t *testing.T) {
	patch := `*** Update File: test.go
-old
+new
*** End Patch`
	_, err := ParsePatch(patch)
	if err == nil {
		t.Fatal("expected error for missing Begin marker")
	}
}

func TestParsePatch_MissingEnd(t *testing.T) {
	patch := `*** Begin Patch
*** Update File: test.go
-old
+new`
	_, err := ParsePatch(patch)
	if err == nil {
		t.Fatal("expected error for missing End marker")
	}
}

func TestParsePatch_Empty(t *testing.T) {
	patch := `*** Begin Patch
*** End Patch`
	_, err := ParsePatch(patch)
	if err == nil {
		t.Fatal("expected error for empty patch")
	}
}

// --- seekSequence tests ---

func TestSeekSequence_Exact(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e"}
	idx := seekSequence(lines, []string{"c", "d"}, 0)
	if idx != 2 {
		t.Errorf("expected 2, got %d", idx)
	}
}

func TestSeekSequence_Rstrip(t *testing.T) {
	lines := []string{"a  ", "b\t", "c"}
	idx := seekSequence(lines, []string{"a", "b"}, 0)
	if idx != 0 {
		t.Errorf("expected 0, got %d", idx)
	}
}

func TestSeekSequence_Trim(t *testing.T) {
	lines := []string{"  a  ", "  b  "}
	idx := seekSequence(lines, []string{"a", "b"}, 0)
	if idx != 0 {
		t.Errorf("expected 0, got %d", idx)
	}
}

func TestSeekSequence_Unicode(t *testing.T) {
	lines := []string{"say \u201chello\u201d"}
	idx := seekSequence(lines, []string{`say "hello"`}, 0)
	if idx != 0 {
		t.Errorf("expected 0, got %d", idx)
	}
}

func TestSeekSequence_NotFound(t *testing.T) {
	lines := []string{"a", "b", "c"}
	idx := seekSequence(lines, []string{"x", "y"}, 0)
	if idx != -1 {
		t.Errorf("expected -1, got %d", idx)
	}
}

func TestSeekSequence_ForwardOnly(t *testing.T) {
	lines := []string{"a", "b", "a", "b"}
	idx := seekSequence(lines, []string{"a", "b"}, 2)
	if idx != 2 {
		t.Errorf("expected 2, got %d", idx)
	}
}

// --- applyPatchChunks tests ---

func TestApplyPatchChunks_SimpleReplace(t *testing.T) {
	content := "line1\nline2\nline3\n"
	chunks := []PatchChunk{{
		Ops: []PatchOp{
			{Type: OpRemove, Line: "line2"},
			{Type: OpAdd, Line: "replaced"},
		},
	}}
	result, err := applyPatchChunks(content, chunks)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "replaced") || strings.Contains(result, "line2") {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestApplyPatchChunks_WithContext(t *testing.T) {
	content := "func main() {\n\told()\n}\n"
	chunks := []PatchChunk{{
		Ops: []PatchOp{
			{Type: OpContext, Line: "func main() {"},
			{Type: OpRemove, Line: "\told()"},
			{Type: OpAdd, Line: "\tnew()"},
			{Type: OpContext, Line: "}"},
		},
	}}
	result, err := applyPatchChunks(content, chunks)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "\tnew()") {
		t.Errorf("replacement not applied: %q", result)
	}
	if !strings.Contains(result, "func main() {") {
		t.Errorf("context line missing: %q", result)
	}
}

func TestApplyPatchChunks_MultipleChunks(t *testing.T) {
	content := "a\nb\nc\nd\ne\n"
	chunks := []PatchChunk{
		{Ops: []PatchOp{{Type: OpRemove, Line: "b"}, {Type: OpAdd, Line: "B"}}},
		{Ops: []PatchOp{{Type: OpRemove, Line: "d"}, {Type: OpAdd, Line: "D"}}},
	}
	result, err := applyPatchChunks(content, chunks)
	if err != nil {
		t.Fatal(err)
	}
	expected := "a\nB\nc\nD\ne\n"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestApplyPatchChunks_NotFound(t *testing.T) {
	content := "a\nb\nc\n"
	chunks := []PatchChunk{{
		Ops: []PatchOp{{Type: OpRemove, Line: "nonexistent"}},
	}}
	_, err := applyPatchChunks(content, chunks)
	if err == nil {
		t.Fatal("expected error for unfound chunk")
	}
}
