package tool

import (
	"fmt"
	"strings"
)

// unifiedDiff produces a minimal unified-diff style output showing the context
// around a single replacement. contextLines controls how many unchanged lines
// surround each change hunk (default 3).
func unifiedDiff(old, new string, contextLines int) string {
	if contextLines <= 0 {
		contextLines = 3
	}

	oldLines := splitLines(old)
	newLines := splitLines(new)

	// Myers is overkill for single-replacement edits. Use a simple LCS-based
	// approach that handles the common case well.
	common := lcs(oldLines, newLines)

	type hunkLine struct {
		op   byte // ' ', '+', '-'
		text string
	}

	// Build full diff sequence
	var all []hunkLine
	oi, ni, ci := 0, 0, 0
	for ci < len(common) {
		// Emit deletions/insertions before next common line
		for oi < common[ci].oldIdx {
			all = append(all, hunkLine{'-', oldLines[oi]})
			oi++
		}
		for ni < common[ci].newIdx {
			all = append(all, hunkLine{'+', newLines[ni]})
			ni++
		}
		all = append(all, hunkLine{' ', oldLines[oi]})
		oi++
		ni++
		ci++
	}
	// Remaining after last common
	for oi < len(oldLines) {
		all = append(all, hunkLine{'-', oldLines[oi]})
		oi++
	}
	for ni < len(newLines) {
		all = append(all, hunkLine{'+', newLines[ni]})
		ni++
	}

	// Extract hunks with context
	// Mark which lines are "interesting" (changed) and expand by contextLines
	interesting := make([]bool, len(all))
	for i, l := range all {
		if l.op != ' ' {
			for j := max(0, i-contextLines); j <= min(len(all)-1, i+contextLines); j++ {
				interesting[j] = true
			}
		}
	}

	var sb strings.Builder
	inHunk := false
	oldLine, newLine := 1, 1
	// Track line numbers
	lineNums := make([][2]int, len(all)) // [oldLine, newLine] at each position
	ol, nl := 1, 1
	for i, l := range all {
		lineNums[i] = [2]int{ol, nl}
		switch l.op {
		case '-':
			ol++
		case '+':
			nl++
		case ' ':
			ol++
			nl++
		}
	}

	for i, l := range all {
		if !interesting[i] {
			if inHunk {
				inHunk = false
			}
			oldLine = lineNums[i][0] + 1
			newLine = lineNums[i][1] + 1
			continue
		}

		if !inHunk {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			fmt.Fprintf(&sb, "@@ -%d +%d @@\n", oldLine, newLine)
			inHunk = true
		}

		ln := lineNums[i]
		switch l.op {
		case '-':
			fmt.Fprintf(&sb, "%4d -%s\n", ln[0], l.text)
		case '+':
			fmt.Fprintf(&sb, "%4d +%s\n", ln[1], l.text)
		default:
			fmt.Fprintf(&sb, "%4d  %s\n", ln[0], l.text)
		}
	}

	return strings.TrimSuffix(sb.String(), "\n")
}

// EditStartLine returns the 1-based line number where oldText starts in
// content, so edit previews can show real file line numbers. It prefers the
// first exact occurrence and falls back to the same fuzzy matching the edit
// tool uses. Returns 1 when oldText is empty or cannot be located (callers
// degrade to numbering from 1).
func EditStartLine(content, oldText string) int {
	if content == "" || oldText == "" {
		return 1
	}
	idx := strings.Index(content, oldText)
	if idx < 0 {
		start, _, _, err := fuzzyFind(content, oldText)
		if err != nil {
			return 1
		}
		idx = start
	}
	return strings.Count(content[:idx], "\n") + 1
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	return lines
}

// lcsMatch records a line common to both old and new.
type lcsMatch struct {
	oldIdx, newIdx int
}

// maxLCSCells caps the DP table size. A single edit in a large file leaves a
// tiny differing region after prefix/suffix trimming, so this only trips when
// two large, wholly-different blobs are compared — where we fall back to a
// delete+insert block rather than allocate an O(n*m) table (30k×30k ≈ 7GB).
const maxLCSCells = 4_000_000

// lcs finds the longest common subsequence of lines. It trims the common
// prefix and suffix first — those lines are trivially part of the LCS — so the
// expensive O(n*m) DP runs only on the region that actually differs.
func lcs(a, b []string) []lcsMatch {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return nil
	}

	var result []lcsMatch

	// Common prefix.
	lo := 0
	for lo < n && lo < m && a[lo] == b[lo] {
		result = append(result, lcsMatch{lo, lo})
		lo++
	}
	// Common suffix (collected in reverse, not overlapping the prefix).
	hiA, hiB := n, m
	var suffix []lcsMatch
	for hiA > lo && hiB > lo && a[hiA-1] == b[hiB-1] {
		hiA--
		hiB--
		suffix = append(suffix, lcsMatch{hiA, hiB})
	}

	// Middle region [lo,hiA) × [lo,hiB) — the only part needing the DP.
	midN, midM := hiA-lo, hiB-lo
	if midN > 0 && midM > 0 && int64(midN)*int64(midM) <= maxLCSCells {
		dp := make([][]int, midN+1)
		for i := range dp {
			dp[i] = make([]int, midM+1)
		}
		for i := midN - 1; i >= 0; i-- {
			for j := midM - 1; j >= 0; j-- {
				if a[lo+i] == b[lo+j] {
					dp[i][j] = dp[i+1][j+1] + 1
				} else {
					dp[i][j] = max(dp[i+1][j], dp[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < midN && j < midM {
			if a[lo+i] == b[lo+j] {
				result = append(result, lcsMatch{lo + i, lo + j})
				i++
				j++
			} else if dp[i+1][j] >= dp[i][j+1] {
				i++
			} else {
				j++
			}
		}
	}
	// A middle over the cell cap contributes no matches — it renders as a
	// delete+insert block, which is correct if less compact.

	for k := len(suffix) - 1; k >= 0; k-- {
		result = append(result, suffix[k])
	}
	return result
}
