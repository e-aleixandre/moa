package tui

import (
	"testing"
)

func TestPickerOpenClose(t *testing.T) {
	p := newPicker()
	if p.active {
		t.Fatal("should not be active initially")
	}

	p.Open("claude-sonnet-4-6", nil)
	if !p.active {
		t.Fatal("should be active after Open")
	}
	if len(p.entries) == 0 {
		t.Fatal("should have entries")
	}

	// Cursor should be on the current model.
	selected := p.Selected()
	if selected.ID != "claude-sonnet-4-6" {
		t.Fatalf("cursor should start on current model, got %s", selected.ID)
	}

	p.Close()
	if p.active {
		t.Fatal("should not be active after Close")
	}
}

func TestPickerNavigation(t *testing.T) {
	p := newPicker()
	p.Open("", nil) // no current model

	initial := p.cursor
	p.MoveDown()
	if p.cursor != initial+1 {
		t.Fatalf("expected cursor at %d, got %d", initial+1, p.cursor)
	}
	p.MoveUp()
	if p.cursor != initial {
		t.Fatalf("expected cursor back at %d, got %d", initial, p.cursor)
	}

	// MoveUp at top should stay at 0.
	p.cursor = 0
	p.MoveUp()
	if p.cursor != 0 {
		t.Fatalf("cursor should stay at 0, got %d", p.cursor)
	}

	// MoveDown at bottom should stay.
	p.cursor = len(p.entries) - 1
	p.MoveDown()
	if p.cursor != len(p.entries)-1 {
		t.Fatalf("cursor should stay at last, got %d", p.cursor)
	}
}

func TestPickerToggleScoped(t *testing.T) {
	p := newPicker()
	p.Open("", nil)

	if p.entries[0].scoped {
		t.Fatal("should not be scoped initially")
	}
	p.ToggleScoped()
	if !p.entries[0].scoped {
		t.Fatal("should be scoped after toggle")
	}
	p.ToggleScoped()
	if p.entries[0].scoped {
		t.Fatal("should not be scoped after second toggle")
	}
}

func TestPickerScopedIDs(t *testing.T) {
	p := newPicker()
	p.Open("", nil)

	// Pin first two models.
	p.ToggleScoped()
	p.MoveDown()
	p.ToggleScoped()

	ids := p.ScopedIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 scoped, got %d", len(ids))
	}
}

func TestPickerRestoresScoped(t *testing.T) {
	p := newPicker()
	p.Open("", nil)

	// Pin first model.
	firstID := p.entries[0].model.ID
	p.ToggleScoped()
	scoped := p.ScopedIDs()

	// Reopen with previous scoped state.
	p.Close()
	p.Open("", scoped)
	if !p.entries[0].scoped {
		t.Fatalf("model %s should be scoped after reopen", firstID)
	}
}

func TestCycleThinkingLevel(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"off", "minimal"},
		{"minimal", "low"},
		{"low", "medium"},
		{"medium", "high"},
		{"high", "off"},
		{"unknown", "medium"},
	}
	for _, tt := range tests {
		got := cycleThinkingLevel(tt.in)
		if got != tt.want {
			t.Errorf("cycleThinkingLevel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPickerView(t *testing.T) {
	p := newPicker()
	p.Open("claude-sonnet-4-6", nil)

	view := p.View(80)
	if view == "" {
		t.Fatal("view should not be empty")
	}

	// Should contain model names.
	if !containsStr(view, "Sonnet") && !containsStr(view, "sonnet") {
		t.Fatal("view should contain a model name")
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
