package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// inputModel wraps textarea for user input.
type inputModel struct {
	textarea textarea.Model
	enabled  bool
}

// Input box styles
var (
	inputBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("8")).
				Padding(0, 1)

	inputActiveBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("12")).
				Padding(0, 1)
)

func newInput() inputModel {
	ta := textarea.New()
	ta.Placeholder = "Ask anything... (Option+Enter for newline)"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0 // no limit
	ta.SetHeight(3)
	ta.Cursor.SetMode(cursor.CursorStatic) // no blink → zero idle CPU

	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle()

	// Ctrl+J inserts newline (works in all terminals + tmux).
	// shift+enter / alt+enter kept as extras for terminals that support them.
	ta.KeyMap.InsertNewline = key.NewBinding(
		key.WithKeys("ctrl+j", "shift+enter", "alt+enter"),
	)

	ta.Focus()
	return inputModel{textarea: ta, enabled: true}
}

// Submit returns the trimmed text and resets the textarea. Empty string if nothing.
func (m *inputModel) Submit() string {
	text := strings.TrimSpace(m.textarea.Value())
	m.textarea.Reset()
	return text
}

// SetEnabled enables/disables input and manages focus.
func (m *inputModel) SetEnabled(enabled bool) {
	m.enabled = enabled
	if enabled {
		m.textarea.Focus()
	} else {
		m.textarea.Blur()
	}
}

// SetWidth sets the textarea width, accounting for border + padding.
func (m *inputModel) SetWidth(width int) {
	// Border (1+1) + padding (1+1) = 4 horizontal characters
	inner := width - 4
	if inner < 10 {
		inner = 10
	}
	m.textarea.SetWidth(inner)
}

// Update passes events to the textarea when enabled.
func (m inputModel) Update(msg tea.Msg) (inputModel, tea.Cmd) {
	if !m.enabled {
		return m, nil
	}
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

func (m inputModel) View() string {
	style := inputActiveBorderStyle
	if !m.enabled {
		style = inputBorderStyle
	}
	return style.Render(m.textarea.View())
}

// --- Command parsing ---

// knownCommands is the whitelist of recognized /commands.
// Unknown /prefixes are treated as regular text (avoids false positives
// with paths like /etc/passwd or URLs).
var knownCommands = map[string]bool{
	"clear": true,
	"exit":  true,
	"quit":  true,
}

// ParseCommand returns (command, true) only if text is a known /command.
func ParseCommand(text string) (string, bool) {
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", false
	}
	cmd := strings.TrimPrefix(fields[0], "/")
	if knownCommands[cmd] {
		return cmd, true
	}
	return "", false
}
