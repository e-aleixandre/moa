package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// statusModel shows a spinner and status text while the agent is working.
type statusModel struct {
	spinner spinner.Model
	text    string
	width   int
}

func newStatus() statusModel {
	s := spinner.New()
	s.Style = spinnerStyle
	return statusModel{spinner: s}
}

// SetText sets the status message. Empty string hides the status bar.
func (m *statusModel) SetText(text string) { m.text = text }

// SetWidth sets the available width.
func (m *statusModel) SetWidth(w int) { m.width = w }

// Update handles spinner ticks. No-op when status is empty.
func (m statusModel) Update(msg tea.Msg) (statusModel, tea.Cmd) {
	if m.text == "" {
		return m, nil
	}
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
}

// View renders the status bar. Returns empty string when idle.
func (m statusModel) View() string {
	if m.text == "" {
		return ""
	}
	return statusStyle.Render(fmt.Sprintf(" %s %s", m.spinner.View(), m.text))
}
