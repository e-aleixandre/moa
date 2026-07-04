package tui

import "testing"

func TestSanitizeTerminalOutput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"OSC 52 clipboard", "\x1b]52;c;Zm9v\x07after", "after"},
		{"title", "\x1b]0;pwn\x07x", "x"},
		{"clear + cursor home", "a\x1b[2J\x1b[Hb", "ab"},
		{"SGR color preserved", "\x1b[31mred\x1b[0m", "\x1b[31mred\x1b[0m"},
		{"BEL + backspace", "a\x07b\x08c", "abc"},
		{"newline/tab preserved", "a\nb\tc", "a\nb\tc"},
		{"UTF-8 accented/CJK preserved", "café ñ 日本", "café ñ 日本"},
		{"C1 CSI encoded as UTF-8 dropped", "a\u009b2Jb", "a2Jb"},
		{"C1 NEL encoded as UTF-8 dropped", "x\u0085y", "xy"},
		{"DCS", "\x1bP1;2q@@@\x1b\\end", "end"},
		{"OSC terminated by ST", "\x1b]0;t\x1b\\z", "z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeTerminalOutput(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeTerminalOutput(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
