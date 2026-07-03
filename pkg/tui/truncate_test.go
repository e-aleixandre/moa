package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

func TestTruncateDisplayShortASCII(t *testing.T) {
	s := "hello"
	if got := truncateDisplay(s, 20); got != s {
		t.Fatalf("expected unchanged %q, got %q", s, got)
	}
	if strings.Contains(truncateDisplay(s, 20), "…") {
		t.Fatal("short ASCII should not be truncated")
	}
}

func TestTruncateDisplayExactBoundary(t *testing.T) {
	s := "exactly-ten"
	w := lipgloss.Width(s)
	if got := truncateDisplay(s, w); got != s {
		t.Fatalf("string at exact width should be unchanged: got %q", got)
	}
}

func TestTruncateDisplaySpanishAccents(t *testing.T) {
	s := "configuración de sesión con muchos acentos áéíóú ñ"
	const max = 20
	got := truncateDisplay(s, max)
	if !utf8.ValidString(got) {
		t.Fatalf("result is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated result should end in ellipsis: %q", got)
	}
	if w := lipgloss.Width(got); w > max {
		t.Fatalf("visual width %d exceeds max %d: %q", w, max, got)
	}
}

func TestTruncateDisplayCJKAndEmoji(t *testing.T) {
	cases := []struct {
		s   string
		max int
	}{
		{"日本語のテキストがとても長い", 8},
		{"🚀🔥🚀🔥🚀🔥🚀🔥", 5},
		{"café 日本語 🚀 mixed content here", 12},
	}
	for _, c := range cases {
		got := truncateDisplay(c.s, c.max)
		if !utf8.ValidString(got) {
			t.Fatalf("result not valid UTF-8 for %q: %q", c.s, got)
		}
		if w := lipgloss.Width(got); w > c.max {
			t.Fatalf("visual width %d exceeds max %d for %q: %q", w, c.max, c.s, got)
		}
	}
}

func TestTruncateDisplayPreservesANSIEscape(t *testing.T) {
	s := "\x1b[31mrojo intenso\x1b[0m un texto rojo bastante largo para cortar"
	got := truncateDisplay(s, 15)
	// The reset sequence must survive so the terminal color is closed.
	if !strings.Contains(got, "\x1b[0m") {
		t.Fatalf("ANSI reset sequence was broken: %q", got)
	}
	if w := lipgloss.Width(got); w > 15 {
		t.Fatalf("visual width %d exceeds max: %q", w, got)
	}
}

func TestTruncateLineWithEmojiTitle(t *testing.T) {
	title := "🚀 Sesión de refactor con un título muy largo"
	got := truncateLine(title, 12)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateLine produced invalid UTF-8: %q", got)
	}
	if w := lipgloss.Width(got); w > 12 {
		t.Fatalf("truncateLine width %d exceeds 12: %q", w, got)
	}
}
