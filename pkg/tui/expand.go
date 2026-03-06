package tui

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// expandModel is an in-process full-screen pager for viewing the conversation.
// Uses Bubble Tea's viewport — no external process, no stdin issues.
type expandModel struct {
	viewport viewport.Model
	ready    bool
}

var expandFooterStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("241")).
	Padding(0, 1)

func newExpand() expandModel {
	return expandModel{}
}

// SetSize configures the viewport dimensions (full terminal minus footer).
func (e *expandModel) SetSize(width, height int) {
	footerHeight := 1
	vpHeight := height - footerHeight
	if vpHeight < 1 {
		vpHeight = 1
	}
	if !e.ready {
		e.viewport = viewport.New(width, vpHeight)
		e.viewport.Style = lipgloss.NewStyle()
		e.ready = true
	} else {
		e.viewport.Width = width
		e.viewport.Height = vpHeight
	}
}

// SetContent populates the viewport with rendered conversation blocks.
func (e *expandModel) SetContent(content string) {
	e.viewport.SetContent(content)
	// Start at the bottom so the user sees the latest messages
	e.viewport.GotoBottom()
}

// Update handles keys in expand mode. Returns true if expand should close.
func (e *expandModel) Update(msg tea.Msg) (bool, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlO:
			return true, nil
		case tea.KeyRunes:
			if string(msg.Runes) == "q" {
				return true, nil
			}
		}
	}
	var cmd tea.Cmd
	e.viewport, cmd = e.viewport.Update(msg)
	return false, cmd
}

// View renders the viewport + footer with scroll position and help.
func (e expandModel) View() string {
	if !e.ready {
		return "Loading..."
	}
	footer := expandFooterStyle.Render(
		fmt.Sprintf(" %3.f%% │ ↑↓ scroll │ q/esc close", e.viewport.ScrollPercent()*100),
	)
	return e.viewport.View() + "\n" + footer
}

// clearScreenDoneMsg signals the clear command finished.
type clearScreenDoneMsg struct{ err error }

// clearScreen clears the terminal screen and scrollback via the system clear(1)
// command. Bypasses BT's renderer so escape sequences don't interfere.
// Falls back to writing CSI escapes directly if clear(1) is not available.
func clearScreen() tea.Cmd {
	clearPath, err := exec.LookPath("clear")
	if err != nil {
		return func() tea.Msg {
			os.Stdout.WriteString("\x1b[3J\x1b[2J\x1b[H")
			return clearScreenDoneMsg{}
		}
	}
	c := exec.Command(clearPath)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return clearScreenDoneMsg{err: err}
	})
}
