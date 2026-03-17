package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const maxHistory = 100

// inputModel wraps textarea for user input with history navigation.
type inputModel struct {
	textarea textarea.Model
	enabled  bool
	history  []string // oldest first
	histIdx  int      // -1 = draft (not navigating)
	draft    string   // saved draft when navigating history
}

// inputBorderStyle and inputActiveBorderStyle are theme-derived.
// Rebuilt by RebuildUI() in styles.go.

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
	return inputModel{textarea: ta, enabled: true, histIdx: -1}
}

// Submit returns the trimmed text, pushes it to history, and resets the textarea.
func (m *inputModel) Submit() string {
	text := strings.TrimSpace(m.textarea.Value())
	m.textarea.Reset()
	m.histIdx = -1
	m.draft = ""
	if text == "" {
		return ""
	}
	// Deduplicate: skip if same as last entry.
	if len(m.history) == 0 || m.history[len(m.history)-1] != text {
		m.history = append(m.history, text)
		if len(m.history) > maxHistory {
			m.history = m.history[len(m.history)-maxHistory:]
		}
	}
	return text
}

// HistoryUp navigates to the previous history entry.
// Returns true if it consumed the key (caller should not propagate).
func (m *inputModel) HistoryUp() bool {
	if len(m.history) == 0 {
		return false
	}
	// Only activate when cursor is on the first line.
	if m.textarea.Line() != 0 {
		return false
	}
	if m.histIdx == -1 {
		// Entering history — save current text as draft.
		m.draft = m.textarea.Value()
		m.histIdx = len(m.history) - 1
	} else if m.histIdx > 0 {
		m.histIdx--
	} else {
		return true // already at oldest, consume but do nothing
	}
	m.textarea.Reset()
	m.textarea.SetValue(m.history[m.histIdx])
	m.textarea.CursorEnd()
	return true
}

// HistoryDown navigates to the next history entry, or back to draft.
// Returns true if it consumed the key.
func (m *inputModel) HistoryDown() bool {
	if m.histIdx == -1 {
		return false // not navigating history
	}
	// Only activate when cursor is on the last line.
	if m.textarea.Line() != m.textarea.LineCount()-1 {
		return false
	}
	m.histIdx++
	if m.histIdx >= len(m.history) {
		// Past newest → restore draft.
		m.histIdx = -1
		m.textarea.Reset()
		m.textarea.SetValue(m.draft)
		m.textarea.CursorEnd()
		m.draft = ""
	} else {
		m.textarea.Reset()
		m.textarea.SetValue(m.history[m.histIdx])
		m.textarea.CursorEnd()
	}
	return true
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
// Derived from allCommands (cmdpalette.go) plus aliases. Unknown /prefixes
// are treated as regular text (avoids false positives with paths like
// /etc/passwd or URLs).
var knownCommands = func() map[string]bool {
	m := make(map[string]bool, len(allCommands)+4)
	for _, cmd := range allCommands {
		m[cmd.Name] = true
	}
	// Aliases not in allCommands.
	m["quit"] = true
	m["models"] = true
	return m
}()

// ParseCommand returns (command string with args, true) only if text starts with a known /command.
// The returned string includes any arguments (e.g., "/model sonnet" → "model sonnet").
func ParseCommand(text string) (string, bool) {
	if !strings.HasPrefix(text, "/") {
		return "", false
	}
	trimmed := strings.TrimSpace(text[1:]) // strip leading "/"
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return "", false
	}
	if knownCommands[fields[0]] {
		return trimmed, true
	}
	return "", false
}
