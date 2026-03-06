package tui

import "testing"

func TestParseCommand(t *testing.T) {
	tests := []struct {
		input string
		cmd   string
		ok    bool
	}{
		{"/clear", "clear", true},
		{"/exit", "exit", true},
		{"/model", "model", true},
		{"/model sonnet", "model sonnet", true},
		{"/models", "models", true},
		{"/thinking", "thinking", true},
		{"/thinking high", "thinking high", true},
		{"/unknown", "", false},
		{"not a command", "", false},
		{"/etc/passwd", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		cmd, ok := ParseCommand(tt.input)
		if ok != tt.ok {
			t.Errorf("ParseCommand(%q) ok=%v, want %v", tt.input, ok, tt.ok)
			continue
		}
		if cmd != tt.cmd {
			t.Errorf("ParseCommand(%q) cmd=%q, want %q", tt.input, cmd, tt.cmd)
		}
	}
}
