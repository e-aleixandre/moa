package tui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// statusModel shows a spinner and status text while the agent is working.
// Always renders exactly one line to avoid layout shifts.
//
// It has two modes. In "activity" mode (phase != "") it renders the agent's
// coarse phase with the Points spinner, a live elapsed counter and, in the
// working phase, a rotating gerund — mirroring the web activity indicator. In
// plain mode it shows a one-off text message (clipboard, voice, transient
// notices).
type statusModel struct {
	spinner spinner.Model
	text    string
	width   int

	phase    string    // "" = plain text mode; otherwise an activity phase
	runStart time.Time // anchor for the elapsed counter (zero = no timer)
}

func newStatus() statusModel {
	s := spinner.New()
	s.Spinner = spinner.Points
	s.Style = spinnerStyle
	return statusModel{spinner: s}
}

// SetText shows a one-off status message and leaves activity mode. Empty string
// clears the line (still occupies one row to prevent layout shifts).
func (m *statusModel) SetText(text string) {
	m.text = text
	m.phase = ""
	m.runStart = time.Time{}
}

// SetPhase enters activity mode for the given phase. runStart anchors the
// elapsed counter; pass the zero time to omit it. The rendered label and timer
// update on every spinner/render tick.
func (m *statusModel) SetPhase(phase string, runStart time.Time) {
	m.phase = phase
	m.runStart = runStart
	m.text = ""
}

// Clear leaves both modes and blanks the line.
func (m *statusModel) Clear() {
	m.text = ""
	m.phase = ""
	m.runStart = time.Time{}
}

// SetWidth sets the available width.
func (m *statusModel) SetWidth(w int) { m.width = w }

// active reports whether the status line has anything to show.
func (m statusModel) active() bool { return m.phase != "" || m.text != "" }

// Update handles spinner ticks. No-op when the line is empty.
func (m statusModel) Update(msg tea.Msg) (statusModel, tea.Cmd) {
	if !m.active() {
		return m, nil
	}
	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
}

// View renders the status line. Always returns exactly one line
// (empty or with content) to keep viewport height stable.
func (m statusModel) View() string {
	if m.phase != "" {
		var elapsed time.Duration
		if !m.runStart.IsZero() {
			elapsed = time.Since(m.runStart)
		}
		label := activityText(m.phase, elapsed)
		// Timer only for the running phases; the momentary
		// compacting/verifying/waiting states read oddly with an age counter.
		if !m.runStart.IsZero() && (m.phase == phaseThinking || m.phase == phaseWorking) {
			label = fmt.Sprintf("%s · %s", label, formatElapsed(elapsed))
		}
		return statusStyle.Render(fmt.Sprintf(" %s %s", m.spinner.View(), label))
	}
	if m.text == "" {
		return " "
	}
	return statusStyle.Render(fmt.Sprintf(" %s %s", m.spinner.View(), m.text))
}
