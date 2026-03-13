package tui

import (
	"testing"
)

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
		{"/plan", "plan", true},
		{"/compact", "compact", true},
		{"/permissions", "permissions", true},
		{"/permissions yolo", "permissions yolo", true},
		{"/tasks", "tasks", true},
		{"/tasks done 3", "tasks done 3", true},
		{"/tasks show all", "tasks show all", true},
		{"/tasks reset", "tasks reset", true},
		{"/verify", "verify", true},
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

func TestInputHistory(t *testing.T) {
	m := newInput()

	// Submit some messages.
	m.textarea.SetValue("hello")
	m.Submit()
	m.textarea.SetValue("world")
	m.Submit()
	m.textarea.SetValue("/clear")
	m.Submit()

	// Navigate up through history.
	if !m.HistoryUp() {
		t.Fatal("HistoryUp should return true")
	}
	if m.textarea.Value() != "/clear" {
		t.Fatalf("expected '/clear', got %q", m.textarea.Value())
	}

	if !m.HistoryUp() {
		t.Fatal("HistoryUp should return true")
	}
	if m.textarea.Value() != "world" {
		t.Fatalf("expected 'world', got %q", m.textarea.Value())
	}

	if !m.HistoryUp() {
		t.Fatal("HistoryUp should return true")
	}
	if m.textarea.Value() != "hello" {
		t.Fatalf("expected 'hello', got %q", m.textarea.Value())
	}

	// At oldest — should still consume but not change.
	if !m.HistoryUp() {
		t.Fatal("HistoryUp at oldest should still consume")
	}
	if m.textarea.Value() != "hello" {
		t.Fatalf("expected 'hello' at oldest, got %q", m.textarea.Value())
	}

	// Navigate back down.
	if !m.HistoryDown() {
		t.Fatal("HistoryDown should return true")
	}
	if m.textarea.Value() != "world" {
		t.Fatalf("expected 'world', got %q", m.textarea.Value())
	}

	// Down past newest → restore draft.
	m.HistoryDown() // → /clear
	if !m.HistoryDown() {
		t.Fatal("HistoryDown past newest should return true")
	}
	// Draft was empty (we submitted before navigating).
	if m.textarea.Value() != "" {
		t.Fatalf("expected empty draft, got %q", m.textarea.Value())
	}
	// Now histIdx should be -1.
	if m.histIdx != -1 {
		t.Fatalf("expected histIdx=-1, got %d", m.histIdx)
	}
}

func TestInputHistory_DraftPreserved(t *testing.T) {
	m := newInput()
	m.textarea.SetValue("first")
	m.Submit()

	// Type a draft, then navigate up.
	m.textarea.SetValue("my draft")
	if !m.HistoryUp() {
		t.Fatal("HistoryUp should consume")
	}
	if m.textarea.Value() != "first" {
		t.Fatalf("expected 'first', got %q", m.textarea.Value())
	}

	// Navigate down restores draft.
	if !m.HistoryDown() {
		t.Fatal("HistoryDown should consume")
	}
	if m.textarea.Value() != "my draft" {
		t.Fatalf("expected 'my draft', got %q", m.textarea.Value())
	}
}

func TestInputHistory_Dedup(t *testing.T) {
	m := newInput()
	m.textarea.SetValue("same")
	m.Submit()
	m.textarea.SetValue("same")
	m.Submit()
	m.textarea.SetValue("same")
	m.Submit()

	if len(m.history) != 1 {
		t.Fatalf("expected 1 entry after dedup, got %d", len(m.history))
	}
}

func TestInputHistory_EmptyNoHistory(t *testing.T) {
	m := newInput()
	if m.HistoryUp() {
		t.Fatal("HistoryUp with no history should return false")
	}
	if m.HistoryDown() {
		t.Fatal("HistoryDown with no navigation should return false")
	}
}

func TestInputHistory_MaxEntries(t *testing.T) {
	m := newInput()
	for i := 0; i < maxHistory+20; i++ {
		m.textarea.SetValue("msg")
		// Bypass dedup by alternating.
		if i%2 == 0 {
			m.textarea.SetValue("even")
		} else {
			m.textarea.SetValue("odd")
		}
		m.Submit()
	}
	if len(m.history) > maxHistory {
		t.Fatalf("expected at most %d entries, got %d", maxHistory, len(m.history))
	}
}
