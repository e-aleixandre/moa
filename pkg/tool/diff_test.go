package tool

import (
	"fmt"
	"strings"
	"testing"
)

func TestUnifiedDiff_LineNumbers(t *testing.T) {
	old := "line1\nline2\nline3\nline4\nline5\nline6\nline7\n"
	new := "line1\nline2\nline3\nchanged4\nline5\nline6\nline7\n"

	diff := unifiedDiff(old, new, 2)
	if diff == "" {
		t.Fatal("expected non-empty diff")
	}

	// Should contain line numbers.
	if !strings.Contains(diff, "   4 -line4") {
		t.Errorf("expected line 4 deletion with number, got:\n%s", diff)
	}
	if !strings.Contains(diff, "   4 +changed4") {
		t.Errorf("expected line 4 addition with number, got:\n%s", diff)
	}
	// Context lines should also have numbers.
	if !strings.Contains(diff, "   2  line2") || !strings.Contains(diff, "   3  line3") {
		t.Errorf("expected context lines with numbers, got:\n%s", diff)
	}
}

func TestUnifiedDiff_LargeFileSingleEditIsCheap(t *testing.T) {
	// Editing one line in a 30k-line file must not allocate an O(n*m) DP
	// table (~7GB before the prefix/suffix trim). Prefix/suffix trimming
	// leaves a 1-line middle region, so this returns instantly.
	var oldB, newB strings.Builder
	const nLines = 30000
	for i := 0; i < nLines; i++ {
		fmt.Fprintf(&oldB, "line %d\n", i)
		if i == 15000 {
			newB.WriteString("CHANGED\n")
		} else {
			fmt.Fprintf(&newB, "line %d\n", i)
		}
	}

	diff := unifiedDiff(oldB.String(), newB.String(), 2)
	if !strings.Contains(diff, "-line 15000") || !strings.Contains(diff, "+CHANGED") {
		t.Errorf("expected the single change in the diff, got:\n%s", diff)
	}
	// The diff must be a small window, not the whole file.
	if lines := strings.Count(diff, "\n"); lines > 50 {
		t.Errorf("expected a compact diff, got %d lines", lines)
	}
}

func TestLCS_HugeDifferingRegionFallsBackWithoutOOM(t *testing.T) {
	// Two large, wholly-different blobs exceed the cell cap: lcs must return
	// (no matches in the middle) instead of allocating a giant table.
	a := make([]string, 3000)
	b := make([]string, 3000)
	for i := range a {
		a[i] = fmt.Sprintf("a%d", i)
		b[i] = fmt.Sprintf("b%d", i)
	}
	// 3000*3000 = 9M cells > maxLCSCells(4M) → fallback, no DP allocation.
	got := lcs(a, b)
	if len(got) != 0 {
		t.Errorf("expected no common matches for disjoint blobs, got %d", len(got))
	}
}

func TestUnifiedDiff_MultipleHunks(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "line" + string(rune('A'+i))
	}
	old := strings.Join(lines, "\n") + "\n"

	modified := make([]string, 20)
	copy(modified, lines)
	modified[2] = "CHANGED_C"
	modified[17] = "CHANGED_R"
	new := strings.Join(modified, "\n") + "\n"

	diff := unifiedDiff(old, new, 2)
	// Should have two @@ markers.
	count := strings.Count(diff, "@@")
	if count < 2 {
		t.Errorf("expected at least 2 hunk headers, got %d in:\n%s", count, diff)
	}
}

func TestUnifiedDiff_NoChange(t *testing.T) {
	text := "same\ncontent\n"
	diff := unifiedDiff(text, text, 3)
	if diff != "" {
		t.Errorf("expected empty diff for identical content, got:\n%s", diff)
	}
}
