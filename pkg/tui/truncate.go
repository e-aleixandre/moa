package tui

import (
	"github.com/charmbracelet/lipgloss"
	reflowtruncate "github.com/muesli/reflow/truncate"
)

// truncateDisplay truncates s to maxWidth visible cells (ANSI-aware,
// wide-rune-aware), appending "…" when truncated. Safe for multibyte text.
func truncateDisplay(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	return reflowtruncate.StringWithTail(s, uint(maxWidth), "…")
}
