package permission

import (
	"strings"
	"testing"
)

func TestParseDecision(t *testing.T) {
	tests := []struct {
		input string
		want  Decision
	}{
		{"APPROVE", DecisionApprove},
		{"approve", DecisionApprove},
		{"DENY", DecisionDeny},
		{"deny", DecisionDeny},
		{"ASK", DecisionAsk},
		{"ask", DecisionAsk},
		// Models sometimes add explanation
		{"APPROVE - this is a safe read operation", DecisionApprove},
		{"DENY - rm -rf is dangerous", DecisionDeny},
		// Unknown defaults to ask
		{"I'm not sure about this", DecisionAsk},
		{"", DecisionAsk},
	}

	for _, tt := range tests {
		got := parseDecision(tt.input)
		if got != tt.want {
			t.Errorf("parseDecision(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestBuildEvalPrompt_ContainsToolInfo(t *testing.T) {
	prompt := buildEvalPrompt("bash", map[string]any{"command": "rm -rf /"}, []string{"Never delete system files"})

	if !strings.Contains(prompt, "bash") {
		t.Error("prompt should contain tool name")
	}
	if !strings.Contains(prompt, "rm -rf /") {
		t.Error("prompt should contain command")
	}
	if !strings.Contains(prompt, "Never delete system files") {
		t.Error("prompt should contain rules")
	}
	if !strings.Contains(prompt, "APPROVE") || !strings.Contains(prompt, "DENY") || !strings.Contains(prompt, "ASK") {
		t.Error("prompt should contain decision options")
	}
}

func TestBuildEvalPrompt_NoRules(t *testing.T) {
	prompt := buildEvalPrompt("write", map[string]any{"path": "test.txt"}, nil)

	if strings.Contains(prompt, "User-provided rules") {
		t.Error("should not show rules section when none provided")
	}
}

func TestBuildEvalPrompt_TruncatesLongArgs(t *testing.T) {
	longContent := make([]byte, 1000)
	for i := range longContent {
		longContent[i] = 'x'
	}
	prompt := buildEvalPrompt("write", map[string]any{"content": string(longContent)}, nil)

	// The arg value (1000 chars) should be truncated to ~500 + "..."
	if strings.Count(prompt, "x") > 510 {
		t.Errorf("prompt should truncate long arg values, got %d x's", strings.Count(prompt, "x"))
	}
}


