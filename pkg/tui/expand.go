package tui

import (
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// clearScreenDoneMsg signals the clear command finished.
type clearScreenDoneMsg struct{ err error }

// clearCmd implements tea.ExecCommand to clear the terminal screen and scrollback.
// Uses tea.Exec so BT is paused during the write — no race with the renderer.
type clearCmd struct {
	stdout io.Writer
}

func (c *clearCmd) Run() error {
	w := c.stdout
	if w == nil {
		w = os.Stdout
	}
	// CSI 3J = clear scrollback, CSI 2J = clear screen, CSI H = cursor home
	_, err := w.Write([]byte("\x1b[3J\x1b[2J\x1b[H"))
	return err
}

func (c *clearCmd) SetStdin(_ io.Reader)  {}
func (c *clearCmd) SetStdout(w io.Writer) { c.stdout = w }
func (c *clearCmd) SetStderr(_ io.Writer) {}

// clearScreen clears the terminal screen and scrollback buffer.
// Uses tea.Exec to pause BT during the write, avoiding races with the renderer.
// CSI 3J is supported by most modern terminals (xterm, iTerm2, alacritty, kitty,
// tmux 3.2+, Windows Terminal, GNOME Terminal, Konsole).
func clearScreen() tea.Cmd {
	return tea.Exec(&clearCmd{}, func(err error) tea.Msg {
		return clearScreenDoneMsg{err: err}
	})
}
