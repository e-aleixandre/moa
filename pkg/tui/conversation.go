package tui

import (
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// conversationModel is a thin wrapper over viewport.Model.
// It only handles display and scroll — no knowledge of message semantics.
type conversationModel struct {
	viewport   viewport.Model
	autoScroll bool // auto-scroll to bottom on new content
	ready      bool // viewport needs WindowSizeMsg before first use
}

func newConversation() conversationModel {
	return conversationModel{autoScroll: true}
}

// SetSize initializes or resizes the viewport.
func (m *conversationModel) SetSize(width, height int) {
	if !m.ready {
		m.viewport = viewport.New(width, height)
		m.viewport.HighPerformanceRendering = false
		m.ready = true
	} else {
		m.viewport.Width = width
		m.viewport.Height = height
	}
}

// SetContent updates the viewport content. If autoScroll is on, scrolls to bottom.
func (m *conversationModel) SetContent(content string) {
	m.viewport.SetContent(content)
	if m.autoScroll {
		m.viewport.GotoBottom()
	}
}

// Update handles scroll events. After any update, autoScroll tracks whether
// the user is at the bottom — if they scroll up, autoScroll stops.
func (m conversationModel) Update(msg tea.Msg) (conversationModel, tea.Cmd) {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	m.autoScroll = m.viewport.AtBottom()
	return m, cmd
}

// View renders the viewport.
func (m conversationModel) View() string {
	if !m.ready {
		return ""
	}
	return m.viewport.View()
}
