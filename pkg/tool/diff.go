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

// lcs finds the longest common subsequence of lines. O(n*m) but fine for
// the typical edit sizes (single replacement in a file).
func lcs(a, b []string) []lcsMatch {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return nil
	}

	// DP table
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}

	// Backtrack
	var result []lcsMatch
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			result = append(result, lcsMatch{i, j})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			i++
		} else {
			j++
		}
	}
	return result
}
