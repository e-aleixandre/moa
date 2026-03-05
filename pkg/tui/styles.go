package tui

import "github.com/charmbracelet/lipgloss"

// Centralized styles. Change here to retheme the TUI.
var (
	// User messages
	userPrefixStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)

	// Thinking
	thinkingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	// Tools
	toolNameStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	toolSuccessStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	toolErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	toolArgsStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	// Status bar
	statusStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	spinnerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))

	// Errors
	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)
