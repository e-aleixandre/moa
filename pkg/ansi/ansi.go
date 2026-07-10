package ansi

import (
	"strings"
	"unicode/utf8"
)

// Strip removes terminal control sequences and keeps printable text, newlines
// and tabs. Use it for untrusted plain text rendered in a terminal.
func Strip(s string) string { return sanitize(s, false) }

// AllowSGR removes terminal control sequences except CSI SGR color sequences.
// Use it after a renderer such as Glamour which intentionally emits styling.
func AllowSGR(s string) string { return sanitize(s, true) }

func sanitize(s string, allowSGR bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			i = sanitizeEscape(s, i, &b, allowSGR)
		case c == '\n' || c == '\t':
			b.WriteByte(c)
			i++
		case c < 0x20 || c == 0x7f:
			i++
		case c < 0x80:
			b.WriteByte(c)
			i++
		default:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				i++
				continue
			}
			if r < 0xa0 {
				i += size
				continue
			}
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	return b.String()
}

func sanitizeEscape(s string, i int, b *strings.Builder, allowSGR bool) int {
	start := i
	i++
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		j := i + 1
		for j < len(s) && isCSIParamByte(s[j]) {
			j++
		}
		if j >= len(s) {
			return j
		}
		final := s[j]
		j++
		if allowSGR && final == 'm' {
			b.WriteString(s[start:j])
		}
		return j
	case ']', 'P', '^', '_':
		return skipUntilTerminator(s, i+1)
	default:
		return i + 1
	}
}

func isCSIParamByte(c byte) bool {
	return (c >= '0' && c <= '9') || c == ';' || c == ':' || c == '?'
}

func skipUntilTerminator(s string, i int) int {
	for i < len(s) {
		if s[i] == 0x07 {
			return i + 1
		}
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
			return i + 2
		}
		i++
	}
	return i
}
