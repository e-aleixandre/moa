package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

// inputModel wraps textarea for user input.
type inputModel struct {
	textarea textarea.Model
	enabled  bool
}

func newInput() inputModel {
	ta := textarea.New()
	ta.Placeholder = "Ask anything... (Ctrl+D to exit)"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0 // no limit
	ta.SetHeight(3)
	ta.Cursor.SetMode(cursor.CursorStatic) // no blink → zero idle CPU
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

// SetWidth sets the textarea width.
func (m *inputModel) SetWidth(width int) {
	m.textarea.SetWidth(width)
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

// View renders the textarea.
func (m inputModel) View() string {
	if !m.enabled {
		return ""
	}
	return m.textarea.View()
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
