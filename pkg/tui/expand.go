package tui

import (
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
)

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
