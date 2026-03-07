package tool

import (
	"strings"
	"testing"
)

func TestUnifiedDiff_SimpleReplace(t *testing.T) {
	old := "line1\nline2\nline3\nline4\nline5"
	new := "line1\nline2\nchanged\nline4\nline5"

	diff := unifiedDiff(old, new, 1)

	if !strings.Contains(diff, "-line3") {
		t.Error("should contain deleted line")
	}
	if !strings.Contains(diff, "+changed") {
		t.Error("should contain added line")
	}
	// Context lines
	if !strings.Contains(diff, " line2") {
		t.Error("should contain context before")
	}
	if !strings.Contains(diff, " line4") {
		t.Error("should contain context after")
	}
}

func TestUnifiedDiff_MultilineReplace(t *testing.T) {
	old := "a\nb\nc\nd\ne"
	new := "a\nX\nY\nd\ne"

	diff := unifiedDiff(old, new, 1)

	if !strings.Contains(diff, "-b") {
		t.Error("should contain -b")
	}
	if !strings.Contains(diff, "-c") {
		t.Error("should contain -c")
	}
	if !strings.Contains(diff, "+X") {
		t.Error("should contain +X")
	}
	if !strings.Contains(diff, "+Y") {
		t.Error("should contain +Y")
	}
}

func TestUnifiedDiff_Insertion(t *testing.T) {
	old := "a\nb"
	new := "a\nnew\nb"

	diff := unifiedDiff(old, new, 1)

	if !strings.Contains(diff, "+new") {
		t.Error("should contain inserted line")
	}
	// Original lines preserved
	if strings.Contains(diff, "-a") || strings.Contains(diff, "-b") {
		t.Error("should not delete original lines")
	}
}

func TestUnifiedDiff_Deletion(t *testing.T) {
	old := "a\nremove\nb"
	new := "a\nb"

	diff := unifiedDiff(old, new, 1)

	if !strings.Contains(diff, "-remove") {
		t.Error("should contain deleted line")
	}
	// No added lines (lines starting with "+", excluding hunk headers)
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "@@") {
			t.Errorf("should not have additions, got: %q", line)
		}
	}
}

func TestUnifiedDiff_NoChange(t *testing.T) {
	text := "a\nb\nc"
	diff := unifiedDiff(text, text, 3)

	if diff != "" {
		t.Errorf("identical text should produce empty diff, got: %q", diff)
	}
}

func TestUnifiedDiff_HunkHeader(t *testing.T) {
	old := "a\nb\nc"
	new := "a\nX\nc"

	diff := unifiedDiff(old, new, 1)

	if !strings.Contains(diff, "@@") {
		t.Error("should contain hunk header")
	}
}
