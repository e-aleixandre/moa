package tui

import "github.com/charmbracelet/lipgloss"

// All TUI styles, derived from ActiveTheme.
// Call RebuildStyles() after changing ActiveTheme.
var (
	// User messages
	userPrefixStyle lipgloss.Style

	// Thinking
	thinkingStyle lipgloss.Style

	// Tool blocks
	toolBlockStyle   lipgloss.Style // left-bordered container
	toolHeaderStyle  lipgloss.Style // tool name in the header
	toolSuccessStyle lipgloss.Style // ✓ icon
	toolErrorStyle   lipgloss.Style // ✗ icon
	toolRunningStyle lipgloss.Style // ● icon
	toolArgsStyle    lipgloss.Style // args summary
	toolResultStyle  lipgloss.Style // result text

	// Status / spinner
	statusStyle  lipgloss.Style
	spinnerStyle lipgloss.Style

	// Errors
	errorStyle lipgloss.Style

	// Status line segments
	statusLineKeyStyle   lipgloss.Style
	statusLineValueStyle lipgloss.Style
	statusLineSepStyle   lipgloss.Style
)

// toolBlockBorder is a minimal left-only border.
var toolBlockBorder = lipgloss.Border{
	Left: "│",
}

// RebuildStyles derives all styles from ActiveTheme.
// Called once at init; call again after swapping ActiveTheme.
func RebuildStyles() {
	t := ActiveTheme

	userPrefixStyle = lipgloss.NewStyle().Foreground(t.Blue).Bold(true)
	thinkingStyle = lipgloss.NewStyle().Foreground(t.Overlay0)

	toolBlockStyle = lipgloss.NewStyle().
		Border(toolBlockBorder, false, false, false, true).
		BorderForeground(t.Surface2).
		PaddingLeft(1).
		MarginLeft(2)
	toolHeaderStyle = lipgloss.NewStyle().Foreground(t.Sapphire).Bold(true)
	toolSuccessStyle = lipgloss.NewStyle().Foreground(t.Green)
	toolErrorStyle = lipgloss.NewStyle().Foreground(t.Red)
	toolRunningStyle = lipgloss.NewStyle().Foreground(t.Yellow)
	toolArgsStyle = lipgloss.NewStyle().Foreground(t.Subtext0)
	toolResultStyle = lipgloss.NewStyle().Foreground(t.Overlay1)

	statusStyle = lipgloss.NewStyle().Foreground(t.Overlay1)
	spinnerStyle = lipgloss.NewStyle().Foreground(t.Sapphire)
	errorStyle = lipgloss.NewStyle().Foreground(t.Red)

	statusLineKeyStyle = lipgloss.NewStyle().Foreground(t.Overlay1)
	statusLineValueStyle = lipgloss.NewStyle().Foreground(t.Text)
	statusLineSepStyle = lipgloss.NewStyle().Foreground(t.Surface2)

	rebuildStatusLineVars()
}

func init() {
	RebuildStyles()
}
