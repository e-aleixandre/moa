package tui

import "testing"

func TestCmdPalette_ActivatesOnSlash(t *testing.T) {
	var p cmdPalette
	p.Update("/")
	if !p.active {
		t.Error("should activate on /")
	}
	if len(p.matches) != len(allCommands) {
		t.Errorf("expected %d matches, got %d", len(allCommands), len(p.matches))
	}
}

func TestCmdPalette_FiltersCommands(t *testing.T) {
	var p cmdPalette
	p.Update("/mod")
	if !p.active {
		t.Error("should be active")
	}
	if len(p.matches) != 1 || p.matches[0].Name != "model" {
		t.Errorf("expected [model], got %v", p.matches)
	}
}

func TestCmdPalette_ClosesOnSpace(t *testing.T) {
	var p cmdPalette
	p.Update("/model sonnet")
	if p.active {
		t.Error("should close when user types args (space)")
	}
}

func TestCmdPalette_ClosesOnNonSlash(t *testing.T) {
	var p cmdPalette
	p.Update("/")
	p.Update("hello")
	if p.active {
		t.Error("should close when text doesn't start with /")
	}
}

func TestCmdPalette_Navigation(t *testing.T) {
	var p cmdPalette
	p.Update("/")

	initial := p.cursor
	p.MoveDown()
	if p.cursor != initial+1 {
		t.Error("MoveDown should increment cursor")
	}
	p.MoveUp()
	if p.cursor != initial {
		t.Error("MoveUp should decrement cursor")
	}
}

func TestCmdPalette_Selected(t *testing.T) {
	var p cmdPalette
	p.Update("/")
	if p.Selected() == "" {
		t.Error("should have a selection")
	}
	// First command
	if p.Selected() != allCommands[0].Name {
		t.Errorf("expected %s, got %s", allCommands[0].Name, p.Selected())
	}
}

func TestCmdPalette_CursorClamped(t *testing.T) {
	var p cmdPalette
	p.Update("/")
	p.cursor = 5
	p.Update("/ex") // filters to just "exit"
	if p.cursor >= len(p.matches) {
		t.Error("cursor should be clamped to matches length")
	}
}

func TestCmdPalette_NoMatchesInactive(t *testing.T) {
	var p cmdPalette
	p.Update("/zzz")
	// Active but no matches is fine — view will be empty
	if p.Selected() != "" {
		t.Error("should return empty for no matches")
	}
}
