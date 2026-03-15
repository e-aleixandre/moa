package main

import "testing"

func TestParseAllowPattern_Valid(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Bash(go:*)", "Bash(go:*)"},
		{"  Write(*.go)  ", "Write(*.go)"},
		{"edit", "edit"},
	}
	for _, tt := range tests {
		got, err := parseAllowPattern(tt.input)
		if err != nil {
			t.Errorf("parseAllowPattern(%q) error: %v", tt.input, err)
		}
		if got != tt.want {
			t.Errorf("parseAllowPattern(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseAllowPattern_Empty(t *testing.T) {
	for _, input := range []string{"", "  ", "\t"} {
		_, err := parseAllowPattern(input)
		if err == nil {
			t.Errorf("parseAllowPattern(%q) should return error", input)
		}
	}
}

func TestParseAllowPattern_Repeated(t *testing.T) {
	// Simulate repeated --allow flags
	var patterns []string
	inputs := []string{"Bash(go:*)", "Write(*.go)", "Bash(npm:*)"}
	for _, val := range inputs {
		parsed, err := parseAllowPattern(val)
		if err != nil {
			t.Fatal(err)
		}
		patterns = append(patterns, parsed)
	}
	if len(patterns) != 3 {
		t.Errorf("expected 3 patterns, got %d", len(patterns))
	}
}
