package tui

import (
	"strings"

	"github.com/e-aleixandre/moa/pkg/ansi"
)

// sanitizeTerminalOutput preserves SGR styling in tool output while removing
// terminal control sequences that could affect the terminal.
func sanitizeTerminalOutput(s string) string {
	return ansi.AllowSGR(s)
}

// sanitizeSessionLabel flattens untrusted label text (e.g. a caller-supplied
// session origin) into single-line plain text: terminal control sequences are
// removed and newlines/tabs collapse to spaces so a label can never split or
// reflow a list row.
func sanitizeSessionLabel(s string) string {
	stripped := ansi.Strip(s)
	return strings.Join(strings.FieldsFunc(stripped, func(r rune) bool {
		return r == '\n' || r == '\t'
	}), " ")
}
