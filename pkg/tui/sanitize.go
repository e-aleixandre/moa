package tui

import (
	"strings"
	"unicode/utf8"
)

// sanitizeTerminalOutput strips terminal control sequences from tool output
// before it is rendered, keeping only what's safe: printable text (including
// multibyte UTF-8), newline/tab, and SGR color escapes (ESC [ ... m). Everything
// else — OSC (e.g. clipboard-writing OSC 52, title changes), DCS/PM/APC, cursor
// movement, screen clears, and other C0 controls — is dropped so a hostile
// bash/curl output can't hijack the terminal.
func sanitizeTerminalOutput(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i, n := 0, len(s)
	for i < n {
		c := s[i]
		switch {
		case c == 0x1b: // ESC — dispatch to the escape-sequence scanner
			i = sanitizeEscape(s, i, &b)
		case c == '\n' || c == '\t':
			b.WriteByte(c)
			i++
		case c < 0x20 || c == 0x7f: // other C0 controls + DEL
			i++
		case c < 0x80:
			b.WriteByte(c)
			i++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				i++ // invalid byte, drop it
				continue
			}
			if r >= 0x80 && r <= 0x9f {
				// C1 control code points (e.g. CSI encoded in UTF-8) — drop the introducer.
				i += size
				continue
			}
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	return b.String()
}

// sanitizeEscape consumes one ESC-prefixed sequence starting at i and returns
// the index just past it. It writes the sequence to b only when it's an SGR
// color escape (CSI ... m); every other escape kind is dropped entirely.
func sanitizeEscape(s string, i int, b *strings.Builder) int {
	n := len(s)
	start := i
	i++ // skip ESC
	if i >= n {
		return i // lone trailing ESC
	}
	switch s[i] {
	case '[': // CSI: params [0-9;:?]* then a final byte 0x40-0x7E
		j := i + 1
		for j < n && isCSIParamByte(s[j]) {
			j++
		}
		if j >= n {
			return j // truncated, no final byte
		}
		final := s[j]
		j++
		if final == 'm' {
			b.WriteString(s[start:j]) // SGR color — keep
		}
		return j
	case ']': // OSC: runs until BEL or ST (ESC \)
		return skipUntilTerminator(s, i+1)
	case 'P', '^', '_': // DCS / PM / APC: run until ST (ESC \)
		return skipUntilTerminator(s, i+1)
	default:
		// Charset selection (ESC (, ESC ), ESC =...), a lone ESC, etc. — drop
		// the ESC and its one associated byte.
		return i + 1
	}
}

// isCSIParamByte reports whether c is a CSI parameter byte.
func isCSIParamByte(c byte) bool {
	return (c >= '0' && c <= '9') || c == ';' || c == ':' || c == '?'
}

// skipUntilTerminator scans from i (just past the introducer) for a BEL
// (0x07) or ST (ESC \) terminator and returns the index just past it. If none
// is found, it returns the end of the string (the rest is dropped).
func skipUntilTerminator(s string, i int) int {
	n := len(s)
	for i < n {
		if s[i] == 0x07 {
			return i + 1
		}
		if s[i] == 0x1b && i+1 < n && s[i+1] == '\\' {
			return i + 2
		}
		i++
	}
	return i
}
