package tool

import (
	"fmt"
	"strings"
)

// fuzzyFind attempts to locate oldText in content using progressively tolerant
// strategies. Returns byte offsets (start, end) of the matching span in content
// and the strategy name used. Returns error if not found or ambiguous.
//
// Strategies (tried in order, first unique match wins):
//  1. exact       — strings.Index
//  2. line-trimmed — TrimSpace each line
//  3. indentation-flexible — strip common leading whitespace
//  4. whitespace-normalized — collapse all whitespace per line
//
// Guardrail: snippets with fewer than 2 non-empty lines skip fuzzy (exact only).
func fuzzyFind(content, oldText string) (start, end int, strategy string, err error) {
	// Strategy 1: exact match
	idx := strings.Index(content, oldText)
	if idx >= 0 {
		// Verify uniqueness
		if strings.Contains(content[idx+1:], oldText) {
			return 0, 0, "", fmt.Errorf("oldText matches multiple locations — be more specific")
		}
		return idx, idx + len(oldText), "exact", nil
	}

	// Guardrail: require at least 2 non-empty lines for fuzzy matching
	oldLines := splitLinesChomp(oldText)
	nonEmpty := 0
	for _, l := range oldLines {
		if strings.TrimSpace(l) != "" {
			nonEmpty++
		}
	}
	if nonEmpty < 2 {
		return 0, 0, "", fmt.Errorf("oldText not found in file")
	}

	contentLines := splitLinesChomp(content)
	n := len(oldLines)
	if n > len(contentLines) {
		return 0, 0, "", fmt.Errorf("oldText not found in file")
	}

	// Strategy 2: line-trimmed
	if s, e, ok := findByWindow(content, contentLines, oldLines, matchLineTrimmed); ok {
		return s, e, "line-trimmed", nil
	}

	// Strategy 3: indentation-flexible
	if s, e, ok := findByWindow(content, contentLines, oldLines, matchIndentFlex); ok {
		return s, e, "indentation-flexible", nil
	}

	// Strategy 4: whitespace-normalized
	if s, e, ok := findByWindow(content, contentLines, oldLines, matchWhitespaceNorm); ok {
		return s, e, "whitespace-normalized", nil
	}

	return 0, 0, "", fmt.Errorf("oldText not found in file (tried exact, line-trimmed, indentation-flexible, whitespace-normalized)")
}

// windowMatcher returns true if contentLines[i:i+n] matches oldLines.
type windowMatcher func(contentLines, oldLines []string) bool

// findByWindow slides a window of len(oldLines) across contentLines using matcher.
// Returns byte offsets (start, end) in the original content if exactly one window matches.
// Returns (0, 0, false) if 0 or >1 matches.
func findByWindow(content string, contentLines, oldLines []string, matcher windowMatcher) (start, end int, ok bool) {
	n := len(oldLines)
	matchCount := 0
	matchIdx := -1

	for i := 0; i <= len(contentLines)-n; i++ {
		if matcher(contentLines[i:i+n], oldLines) {
			matchCount++
			if matchCount > 1 {
				return 0, 0, false // ambiguous
			}
			matchIdx = i
		}
	}

	if matchCount != 1 {
		return 0, 0, false
	}

	// Convert line index to byte offset
	s, e := lineSpanToByteOffsets(content, contentLines, matchIdx, n)
	return s, e, true
}

// lineSpanToByteOffsets converts a line range [lineIdx, lineIdx+count) to byte offsets
// in the original content string.
func lineSpanToByteOffsets(content string, contentLines []string, lineIdx, count int) (start, end int) {
	// Walk through content to find byte positions of each line
	pos := 0
	for i := 0; i < lineIdx; i++ {
		pos += len(contentLines[i]) + 1 // +1 for \n
	}
	start = pos
	for i := lineIdx; i < lineIdx+count; i++ {
		pos += len(contentLines[i]) + 1
	}
	end = pos
	// Trim trailing \n if we're at end of content without final newline
	if end > len(content) {
		end = len(content)
	}
	// The span should include the last line but not its trailing newline if it's the last window
	// Actually, we want the span to be replaceable, so include trailing \n for all but possibly the last
	// Let's be precise: rebuild the span from the content
	// Simple approach: the span is content[start:end] with possible trailing newline adjustment
	if end > len(content) {
		end = len(content)
	}
	return start, end
}

// matchLineTrimmed compares lines after TrimSpace on each.
func matchLineTrimmed(contentLines, oldLines []string) bool {
	for i, ol := range oldLines {
		if strings.TrimSpace(contentLines[i]) != strings.TrimSpace(ol) {
			return false
		}
	}
	return true
}

// matchIndentFlex strips the common indentation from both sides and compares.
func matchIndentFlex(contentLines, oldLines []string) bool {
	cStripped := stripCommonIndent(contentLines)
	oStripped := stripCommonIndent(oldLines)
	if len(cStripped) != len(oStripped) {
		return false
	}
	for i := range cStripped {
		if cStripped[i] != oStripped[i] {
			return false
		}
	}
	return true
}

// matchWhitespaceNorm collapses whitespace per line and compares.
func matchWhitespaceNorm(contentLines, oldLines []string) bool {
	for i, ol := range oldLines {
		if collapseWhitespace(contentLines[i]) != collapseWhitespace(ol) {
			return false
		}
	}
	return true
}

// splitLinesChomp splits text into lines, removing a trailing empty line
// caused by a final newline: "a\nb\n" → ["a", "b"] not ["a", "b", ""].
// This is used for fuzzy matching where trailing-newline artifacts cause
// off-by-one window mismatches. The package-level splitLines (in diff.go)
// preserves the trailing empty element.
func splitLinesChomp(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	// Remove trailing empty element from final newline
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// collapseWhitespace replaces all runs of whitespace with a single space and trims.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// stripCommonIndent removes the minimum leading whitespace from all non-empty lines.
func stripCommonIndent(lines []string) []string {
	// Find minimum indentation across non-empty lines
	minIndent := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		indent := len(l) - len(strings.TrimLeft(l, " \t"))
		if minIndent < 0 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent <= 0 {
		// No common indent to strip
		result := make([]string, len(lines))
		copy(result, lines)
		return result
	}

	result := make([]string, len(lines))
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			result[i] = ""
		} else if len(l) >= minIndent {
			result[i] = l[minIndent:]
		} else {
			result[i] = l
		}
	}
	return result
}

// adjustIndentation computes the indentation transform between the real file
// lines and the model's oldText lines, then applies it to newText lines.
//
// Algorithm:
//  1. Compute common leading whitespace prefix of realLines (non-empty lines)
//  2. Compute common leading whitespace prefix of oldLines (non-empty lines)
//  3. If both are homogeneous (all spaces or all tabs), compute delta
//  4. Apply delta to each non-empty line of newLines
//  5. Mixed tabs/spaces → return newLines unchanged (safety)
func adjustIndentation(realLines, oldLines, newLines []string) []string {
	realPrefix := commonLeadingPrefix(realLines)
	oldPrefix := commonLeadingPrefix(oldLines)

	if realPrefix == oldPrefix {
		// No adjustment needed
		return newLines
	}

	// Check homogeneity
	if !isHomogeneous(realPrefix) || !isHomogeneous(oldPrefix) {
		// Mixed tabs/spaces — don't transform
		return newLines
	}

	// Check they use the same character type (or one is empty)
	realChar := indentChar(realPrefix)
	oldChar := indentChar(oldPrefix)
	if realChar != 0 && oldChar != 0 && realChar != oldChar {
		// Different indent characters — don't transform
		return newLines
	}

	// Compute delta
	delta := len(realPrefix) - len(oldPrefix)
	ch := realChar
	if ch == 0 {
		ch = oldChar
	}
	if ch == 0 {
		ch = ' ' // default to spaces
	}

	result := make([]string, len(newLines))
	for i, line := range newLines {
		if strings.TrimSpace(line) == "" {
			result[i] = line // preserve empty/whitespace-only lines
			continue
		}
		if delta > 0 {
			result[i] = strings.Repeat(string(ch), delta) + line
		} else {
			// Remove |delta| characters from the beginning
			remove := -delta
			if remove > len(line) {
				remove = len(line)
			}
			// Only remove indent characters, not content
			trimmed := 0
			for trimmed < remove && trimmed < len(line) && (line[trimmed] == ' ' || line[trimmed] == '\t') {
				trimmed++
			}
			result[i] = line[trimmed:]
		}
	}
	return result
}

// commonLeadingPrefix returns the longest common leading whitespace prefix
// across all non-empty lines.
func commonLeadingPrefix(lines []string) string {
	var prefix string
	first := true
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		indent := leadingWhitespace(l)
		if first {
			prefix = indent
			first = false
			continue
		}
		prefix = commonPrefix(prefix, indent)
	}
	return prefix
}

// leadingWhitespace returns the leading whitespace of a string.
func leadingWhitespace(s string) string {
	for i, ch := range s {
		if ch != ' ' && ch != '\t' {
			return s[:i]
		}
	}
	return s
}

// commonPrefix returns the longest common prefix of two strings.
func commonPrefix(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a[:n]
}

// isHomogeneous returns true if the string contains only one type of whitespace
// (all spaces or all tabs) or is empty.
func isHomogeneous(s string) bool {
	if s == "" {
		return true
	}
	ch := s[0]
	for i := 1; i < len(s); i++ {
		if s[i] != ch {
			return false
		}
	}
	return true
}

// indentChar returns the indent character used (space or tab), or 0 if empty.
func indentChar(s string) byte {
	if s == "" {
		return 0
	}
	return s[0]
}

// applyEdit applies a single edit to content. Tries exact match first,
// then fuzzy matching if exact fails. Returns (newContent, message, error).
// If replaceAll is true, only exact matching is used (no fuzzy).
// This is the shared function used by both edit and multiedit tools.
func applyEdit(content, oldText, newText string, replaceAll bool) (string, string, error) {
	count := strings.Count(content, oldText)

	// Exact match: single occurrence
	if count == 1 {
		newContent := strings.Replace(content, oldText, newText, 1)
		return newContent, "exact match", nil
	}

	// Exact match: multiple occurrences
	if count > 1 {
		if replaceAll {
			newContent := strings.ReplaceAll(content, oldText, newText)
			return newContent, fmt.Sprintf("replaced all %d occurrences", count), nil
		}
		return "", "", fmt.Errorf("oldText matches %d locations — be more specific", count)
	}

	// count == 0: try fuzzy matching (not for replaceAll)
	if replaceAll {
		return "", "", fmt.Errorf("oldText not found in file")
	}

	start, end, strategy, err := fuzzyFind(content, oldText)
	if err != nil {
		return "", "", err
	}

	realMatch := content[start:end]
	realLines := splitLinesChomp(realMatch)
	oldLines := splitLinesChomp(oldText)
	newLines := splitLinesChomp(newText)

	// Adjust indentation of newText to match the file's indentation
	adjustedNew := adjustIndentation(realLines, oldLines, newLines)

	// Rebuild the new text with adjusted indentation.
	// Preserve trailing newline based on newText intent, not realMatch.
	adjustedNewText := strings.Join(adjustedNew, "\n")
	if strings.HasSuffix(newText, "\n") && !strings.HasSuffix(adjustedNewText, "\n") {
		adjustedNewText += "\n"
	}

	newContent := content[:start] + adjustedNewText + content[end:]
	return newContent, fmt.Sprintf("fuzzy match (%s)", strategy), nil
}
