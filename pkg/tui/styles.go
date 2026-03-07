package tui

import "github.com/charmbracelet/lipgloss"

// All TUI styles, derived from ActiveTheme.
// Call RebuildUI() after changing ActiveTheme (startup-time only).
var (
	// Status / spinner
	statusStyle  lipgloss.Style
	spinnerStyle lipgloss.Style



	// Status line segments
	statusLineKeyStyle   lipgloss.Style
	statusLineValueStyle lipgloss.Style
	statusLineSepStyle   lipgloss.Style

	// Input box
	inputBorderStyle       lipgloss.Style
	inputActiveBorderStyle lipgloss.Style

	// Model picker
	pickerBorderStyle   lipgloss.Style
	pickerSelectedStyle lipgloss.Style
	pickerScopedStyle   lipgloss.Style
	pickerDimStyle      lipgloss.Style
	pickerHeaderStyle   lipgloss.Style
)

// RebuildUI derives all styles from ActiveTheme.
// Called once at init; call again after swapping ActiveTheme at startup.
func RebuildUI() {
	t := ActiveTheme

	statusStyle = lipgloss.NewStyle().Foreground(t.Overlay1)
	spinnerStyle = lipgloss.NewStyle().Foreground(t.Sapphire)
	statusLineKeyStyle = lipgloss.NewStyle().Foreground(t.Overlay1)
	statusLineValueStyle = lipgloss.NewStyle().Foreground(t.Text)
	statusLineSepStyle = lipgloss.NewStyle().Foreground(t.Surface2)

	// Input
	inputBorderStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Surface2).
		Padding(0, 1)
	inputActiveBorderStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Lavender).
		Padding(0, 1)

	// Picker
	pickerBorderStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Surface2).
		Padding(0, 1)
	pickerSelectedStyle = lipgloss.NewStyle().Bold(true).Foreground(t.Lavender)
	pickerScopedStyle = lipgloss.NewStyle().Foreground(t.Green)
	pickerDimStyle = lipgloss.NewStyle().Foreground(t.Overlay0)
	pickerHeaderStyle = lipgloss.NewStyle().Foreground(t.Overlay0).Italic(true)

	// Glamour markdown
	glamourStyleJSON = buildGlamourStyle(t)

	rebuildStatusLineVars()
}

// RebuildStyles is a backward-compatible alias for RebuildUI.
func RebuildStyles() { RebuildUI() }

func init() {
	RebuildUI()
}
