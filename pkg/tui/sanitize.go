package tui

import "github.com/ealeixandre/moa/pkg/ansi"

// sanitizeTerminalOutput preserves SGR styling in tool output while removing
// terminal control sequences that could affect the terminal.
func sanitizeTerminalOutput(s string) string {
	return ansi.AllowSGR(s)
}
