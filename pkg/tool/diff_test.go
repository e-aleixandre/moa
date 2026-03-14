package tool

import (
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
