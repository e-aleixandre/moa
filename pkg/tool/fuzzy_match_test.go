package tool

import (
	"strings"
	"testing"
)

func TestFuzzyFind_Exact(t *testing.T) {
	content := "func main() {\n\tfmt.Println(\"hello\")\n}\n"
	old := "func main() {\n\tfmt.Println(\"hello\")\n}"
	s, e, strategy, err := fuzzyFind(content, old)
	if err != nil {
		t.Fatal(err)
	}
	if strategy != "exact" {
		t.Errorf("expected exact, got %s", strategy)
	}
	if content[s:e] != old {
		t.Errorf("span mismatch: %q", content[s:e])
	}
}

func TestFuzzyFind_ExactMultiple(t *testing.T) {
	content := "x = 1\nx = 1\n"
	_, _, _, err := fuzzyFind(content, "x = 1")
	if err == nil {
		t.Fatal("expected error for multiple matches")
	}
	if !strings.Contains(err.Error(), "multiple") {
		t.Errorf("expected 'multiple' in error, got: %v", err)
	}
}

func TestFuzzyFind_TrailingWhitespace(t *testing.T) {
	// File has no trailing spaces, model sends trailing spaces
	content := "func foo() {\n\tx := 1\n\ty := 2\n}\n"
	old := "func foo() {  \n\tx := 1  \n\ty := 2  \n}  "
	s, e, strategy, err := fuzzyFind(content, old)
	if err != nil {
		t.Fatal(err)
	}
	if strategy != "line-trimmed" {
		t.Errorf("expected line-trimmed, got %s", strategy)
	}
	_ = content[s:e] // should not panic
}

func TestFuzzyFind_LeadingSpacesDifferent(t *testing.T) {
	// File has tabs, model sends spaces (but content is otherwise the same after trim)
	content := "func bar() {\n\ta := 10\n\tb := 20\n}\n"
	old := "func bar() {\n    a := 10\n    b := 20\n}"
	s, e, strategy, err := fuzzyFind(content, old)
	if err != nil {
		t.Fatal(err)
	}
	// line-trimmed should catch this since TrimSpace handles both
	if strategy != "line-trimmed" {
		t.Errorf("expected line-trimmed, got %s", strategy)
	}
	_ = content[s:e]
}

func TestFuzzyFind_IndentationFlexible(t *testing.T) {
	// Two blocks have identical content after TrimSpace → line-trimmed is ambiguous.
	// But they differ in relative indentation → indentation-flexible can distinguish.
	// Block 1: base=4, inner=8 → relative indent of 4
	// Block 2: base=4, inner=12 → relative indent of 8
	content := "    process(x)\n        handle(x)\n    process(x)\n            handle(x)\n"
	// model sends block with 0 base and 4 relative indent → matches block 1 only
	old := "process(x)\n    handle(x)"
	s, e, strategy, err := fuzzyFind(content, old)
	if err != nil {
		t.Fatal(err)
	}
	if strategy != "indentation-flexible" {
		t.Errorf("expected indentation-flexible, got %s", strategy)
	}
	got := content[s:e]
	// Should match the first block (lines 0-1), not the second (lines 2-3)
	if !strings.HasPrefix(got, "    process(x)\n        handle(x)") {
		t.Errorf("matched wrong block: %q", got)
	}
}

func TestFuzzyFind_TabsVsSpaces(t *testing.T) {
	// File uses tabs, model uses spaces — content otherwise identical
	content := "func run() {\n\tif true {\n\t\tgo()\n\t}\n}\n"
	old := "func run() {\n  if true {\n    go()\n  }\n}"
	_, _, strategy, err := fuzzyFind(content, old)
	if err != nil {
		t.Fatal(err)
	}
	// line-trimmed should work here since trimming each line should match
	if strategy != "line-trimmed" {
		t.Errorf("expected line-trimmed, got %s", strategy)
	}
}

func TestFuzzyFind_ExtraSpacesBetweenTokens(t *testing.T) {
	content := "x = foo(a, b)\ny = bar(c, d)\nz = baz(e, f)\n"
	old := "x  =  foo(a,  b)\ny  =  bar(c,  d)\nz  =  baz(e,  f)"
	_, _, strategy, err := fuzzyFind(content, old)
	if err != nil {
		t.Fatal(err)
	}
	if strategy != "whitespace-normalized" {
		t.Errorf("expected whitespace-normalized, got %s", strategy)
	}
}

func TestFuzzyFind_SingleLineGuardrail(t *testing.T) {
	// Single non-empty line → no fuzzy, only exact
	// Use content where exact substring won't match either
	content := "  x_val = 1\ny = 2\n"
	old := "x_val=1" // would fuzzy match after whitespace-norm, but guardrail blocks it
	_, _, _, err := fuzzyFind(content, old)
	if err == nil {
		t.Fatal("expected error for single-line fuzzy attempt")
	}
}

func TestFuzzyFind_NotFound(t *testing.T) {
	content := "func main() {\n\tfmt.Println(\"hello\")\n}\n"
	old := "func totally_different() {\n\tunrelated_code()\n}"
	_, _, _, err := fuzzyFind(content, old)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestFuzzyFind_MultipleFuzzyMatches(t *testing.T) {
	// Two identical blocks (after trim) — should fail all fuzzy strategies
	content := "  x := 1\n  y := 2\nsome other stuff\n  x := 1\n  y := 2\n"
	old := "x := 1\ny := 2"
	_, _, _, err := fuzzyFind(content, old)
	if err == nil {
		t.Fatal("expected error for ambiguous fuzzy matches")
	}
}

// --- adjustIndentation tests ---

func TestAdjustIndentation_DeltaPositive(t *testing.T) {
	// File is more indented than model's oldText
	real := []string{"    func foo() {", "        x := 1", "    }"}
	old := []string{"func foo() {", "    x := 1", "}"}
	new := []string{"func foo() {", "    x := 2", "    y := 3", "}"}
	result := adjustIndentation(real, old, new)
	expected := []string{"    func foo() {", "        x := 2", "        y := 3", "    }"}
	assertLines(t, expected, result)
}

func TestAdjustIndentation_DeltaNegative(t *testing.T) {
	// Model sent more indented than file
	real := []string{"func foo() {", "    x := 1", "}"}
	old := []string{"    func foo() {", "        x := 1", "    }"}
	new := []string{"    func foo() {", "        x := 2", "    }"}
	result := adjustIndentation(real, old, new)
	expected := []string{"func foo() {", "    x := 2", "}"}
	assertLines(t, expected, result)
}

func TestAdjustIndentation_MixedTabsSpaces(t *testing.T) {
	// Mixed → no transform
	real := []string{"\t func foo() {", "\t     x := 1", "\t }"}
	old := []string{"func foo() {", "    x := 1", "}"}
	new := []string{"func bar() {", "    x := 2", "}"}
	result := adjustIndentation(real, old, new)
	// Should be unchanged (no transform)
	assertLines(t, new, result)
}

func TestAdjustIndentation_EmptyLinesPreserved(t *testing.T) {
	real := []string{"    func foo() {", "", "        x := 1", "    }"}
	old := []string{"func foo() {", "", "    x := 1", "}"}
	new := []string{"func foo() {", "", "    x := 2", "}"}
	result := adjustIndentation(real, old, new)
	expected := []string{"    func foo() {", "", "        x := 2", "    }"}
	assertLines(t, expected, result)
}

func TestAdjustIndentation_ZeroToFourSpaces(t *testing.T) {
	real := []string{"    x := 1", "    y := 2"}
	old := []string{"x := 1", "y := 2"}
	new := []string{"x := 3", "y := 4"}
	result := adjustIndentation(real, old, new)
	expected := []string{"    x := 3", "    y := 4"}
	assertLines(t, expected, result)
}

// --- applyEdit tests ---

func TestApplyEdit_ExactSingle(t *testing.T) {
	content := "hello world\nfoo bar\n"
	newContent, msg, err := applyEdit(content, "foo bar", "baz qux", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "exact") {
		t.Errorf("expected 'exact' in msg, got %s", msg)
	}
	if !strings.Contains(newContent, "baz qux") {
		t.Errorf("replacement not applied")
	}
}

func TestApplyEdit_ExactMultiple_Error(t *testing.T) {
	content := "x = 1\nx = 1\n"
	_, _, err := applyEdit(content, "x = 1", "x = 2", false)
	if err == nil {
		t.Fatal("expected error for multiple matches")
	}
}

func TestApplyEdit_ReplaceAll(t *testing.T) {
	content := "x = 1\nx = 1\nx = 1\n"
	newContent, msg, err := applyEdit(content, "x = 1", "x = 2", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "3 occurrences") {
		t.Errorf("expected '3 occurrences' in msg, got %s", msg)
	}
	if strings.Contains(newContent, "x = 1") {
		t.Errorf("not all occurrences replaced")
	}
}

func TestApplyEdit_FuzzyWithIndentAdjust(t *testing.T) {
	content := "class Foo:\n    def bar(self):\n        return 1\n    def baz(self):\n        return 2\n"
	old := "def bar(self):\n    return 1"
	new := "def bar(self):\n    return 42"
	newContent, msg, err := applyEdit(content, old, new, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "fuzzy") {
		t.Errorf("expected fuzzy in msg, got %s", msg)
	}
	if !strings.Contains(newContent, "        return 42") {
		t.Errorf("indentation not adjusted: %s", newContent)
	}
}

func TestApplyEdit_NotFound(t *testing.T) {
	content := "hello world\n"
	_, _, err := applyEdit(content, "nonexistent\nstuff here", "replacement", false)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestApplyEdit_FuzzyDeletion(t *testing.T) {
	// Fuzzy match with empty newText should delete the block without extra blank lines
	content := "before\n    x := 1\n    y := 2\nafter\n"
	old := "x := 1\ny := 2" // fuzzy: indentation-flexible
	newContent, msg, err := applyEdit(content, old, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "fuzzy") {
		t.Errorf("expected fuzzy in msg, got %s", msg)
	}
	// Should not have trailing newline artifacts from the deleted block
	if strings.Contains(newContent, "    \n") {
		t.Errorf("unexpected indented blank line in result: %q", newContent)
	}
}

func TestApplyEdit_ReplaceAll_NotFound(t *testing.T) {
	_, _, err := applyEdit("hello\n", "missing", "x", true)
	if err == nil {
		t.Fatal("expected error")
	}
}

func assertLines(t *testing.T, expected, actual []string) {
	t.Helper()
	if len(expected) != len(actual) {
		t.Errorf("line count: expected %d, got %d\nexpected: %v\nactual: %v", len(expected), len(actual), expected, actual)
		return
	}
	for i := range expected {
		if expected[i] != actual[i] {
			t.Errorf("line %d: expected %q, got %q", i, expected[i], actual[i])
		}
	}
}
