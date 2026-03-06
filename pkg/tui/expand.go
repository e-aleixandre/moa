package tui

import (
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// expandConversation opens the full conversation in less -R.
// Renders all blocks (including thinking if showThinking), pipes to less.
// Disabled while a run is active (caller must check).
func expandConversation(blocks []messageBlock, r *renderer, showThinking bool) tea.Cmd {
	content := renderBlocks(blocks, r, showThinking)
	c := exec.Command("less", "-R", "--quit-if-one-screen")
	c.Stdin = strings.NewReader(content)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return expandDoneMsg{err: err}
	})
}

// clearScreenMsg signals the clear command finished.
type clearScreenDoneMsg struct{ err error }

// clearScreen clears the terminal screen and scrollback via the system clear(1)
// command. Bypasses BT's renderer so escape sequences don't interfere.
// Falls back to writing CSI escapes directly if clear(1) is not available.
func clearScreen() tea.Cmd {
	clearPath, err := exec.LookPath("clear")
	if err != nil {
		// Fallback: write escapes directly to stdout (outside BT renderer)
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
