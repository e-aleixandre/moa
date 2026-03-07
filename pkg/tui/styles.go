package tui

import "github.com/charmbracelet/lipgloss"

// All TUI styles, derived from ActiveTheme.
// Call RebuildStyles() after changing ActiveTheme.
var (
	// User messages
	userPrefixStyle lipgloss.Style

	// Thinking
	thinkingStyle lipgloss.Style

	// Tool blocks — full-width panels with subtle background
	toolBlockStyle     lipgloss.Style // outer container
	toolActionStyle    lipgloss.Style // verb: "write", "bash", "read"
	toolTargetStyle    lipgloss.Style // path, command, query
	toolBodyStyle      lipgloss.Style // code / result content
	toolErrorBodyStyle lipgloss.Style // error content
	toolFooterStyle    lipgloss.Style // "… (N more lines, M total, ctrl+o to expand)"
	toolDimStyle       lipgloss.Style // "running…"

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

// RebuildStyles derives all styles from ActiveTheme.
// Called once at init; call again after swapping ActiveTheme.
func RebuildStyles() {
	t := ActiveTheme

	userPrefixStyle = lipgloss.NewStyle().Foreground(t.Blue).Bold(true)
	thinkingStyle = lipgloss.NewStyle().Foreground(t.Overlay0)

	toolBlockStyle = lipgloss.NewStyle().
		Background(t.Surface0).
		Padding(0, 2)
	toolActionStyle = lipgloss.NewStyle().Foreground(t.Green).Bold(true)
	toolTargetStyle = lipgloss.NewStyle().Foreground(t.Peach)
	toolBodyStyle = lipgloss.NewStyle().Foreground(t.Subtext1)
	toolErrorBodyStyle = lipgloss.NewStyle().Foreground(t.Maroon)
	toolFooterStyle = lipgloss.NewStyle().Foreground(t.Overlay0).Italic(true)
	toolDimStyle = lipgloss.NewStyle().Foreground(t.Overlay0)

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
