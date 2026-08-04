package tui

import (
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestPickerOpenClose(t *testing.T) {
	model := core.ListModels()[0].Model
	p := newPicker()
	if p.active {
		t.Fatal("should not be active initially")
	}
	p.Open(model.ID, nil)
	if !p.active || len(p.entries) == 0 || len(p.providers) == 0 {
		t.Fatal("Open should populate and activate the picker")
	}
	if p.level != pickerRoot {
		t.Fatalf("level = %v, want root", p.level)
	}
	p.Close()
	if p.active {
		t.Fatal("should not be active after Close")
	}
}

func TestPickerRootDrillsIntoProviderThenSelectsModel(t *testing.T) {
	p := newPicker()
	p.Open("", nil)
	p.cursor = rootProviderCursor(t, p, 0)
	wantProvider := p.providers[p.root[p.cursor].provider].name

	if selected, ok := p.Activate(); ok || selected.ID != "" {
		t.Fatalf("provider activation = (%+v, %v), want drill", selected, ok)
	}
	if p.level != pickerProviderDetail || p.providers[p.detailProvider].name != wantProvider {
		t.Fatalf("did not open provider %q", wantProvider)
	}
	want := p.Selected()
	selected, ok := p.Activate()
	if !ok || selected.ID != want.ID {
		t.Fatalf("model activation = (%+v, %v), want %q", selected, ok, want.ID)
	}
}

func TestPickerPinBehaviorAndRootCursorClamp(t *testing.T) {
	model := core.ListModels()[0].Model
	p := newPicker()
	p.Open(model.ID, map[string]bool{model.ID: true})
	if p.root[p.cursor].kind != pickerPinnedModel {
		t.Fatal("current pinned model should be selected in root")
	}
	p.ToggleScoped()
	if p.ScopedIDs()[model.ID] {
		t.Fatal("space on a root pinned model should unpin it")
	}
	if p.cursor < 0 || p.cursor >= len(p.root) {
		t.Fatalf("cursor %d is not clamped to %d root rows", p.cursor, len(p.root))
	}
	if p.root[p.cursor].kind != pickerProvider {
		t.Fatal("unpinning the only pinned model should leave a provider row selected")
	}

	before := p.ScopedIDs()
	p.ToggleScoped()
	if len(p.ScopedIDs()) != len(before) {
		t.Fatal("space on a root provider row should be a no-op")
	}
	p.Activate()
	p.ToggleScoped()
	selected := p.Selected()
	if !p.ScopedIDs()[selected.ID] {
		t.Fatal("space in provider detail should pin the selected model")
	}
}

func TestPickerBackReturnsToRootProvider(t *testing.T) {
	p := newPicker()
	p.Open("", nil)
	p.cursor = rootProviderCursor(t, p, 0)
	wantCursor := p.cursor
	p.Activate()
	if !p.Back() {
		t.Fatal("Back should leave provider detail")
	}
	if p.level != pickerRoot || p.cursor != wantCursor {
		t.Fatalf("Back = level %v cursor %d, want root cursor %d", p.level, p.cursor, wantCursor)
	}
	if p.Back() {
		t.Fatal("Back at root should not close or change level")
	}
}

func TestPickerDetailPinChangesAppearAtRoot(t *testing.T) {
	p := newPicker()
	p.Open("", nil)
	p.cursor = rootProviderCursor(t, p, 0)
	p.Activate()
	p.ToggleScoped()
	pinnedID := p.Selected().ID
	if !p.Back() {
		t.Fatal("Back should leave provider detail")
	}
	if !rootContainsPinnedModel(p, pinnedID) {
		t.Fatalf("root does not contain newly pinned model %q", pinnedID)
	}
	if row := p.root[p.cursor]; row.kind != pickerProvider || row.provider != 0 {
		t.Fatalf("cursor = %+v, want provider 0 after pin changed root length", row)
	}

	p.Activate()
	p.ToggleScoped()
	p.Back()
	if rootContainsPinnedModel(p, pinnedID) {
		t.Fatalf("root still contains unpinned model %q", pinnedID)
	}
}

func rootContainsPinnedModel(p pickerModel, id string) bool {
	for _, row := range p.root {
		if row.kind == pickerPinnedModel && p.entries[row.entry].model.ID == id {
			return true
		}
	}
	return false
}

func TestPickerEmptyPinsRendersProviderOverview(t *testing.T) {
	p := newPicker()
	p.Open("", nil)
	if len(p.root) != len(p.providers) {
		t.Fatalf("root rows = %d, want one row for each of %d providers", len(p.root), len(p.providers))
	}
	view := p.View(100)
	for _, want := range []string{"PINNED", "No pinned models", "ALL MODELS"} {
		if !strings.Contains(view, want) {
			t.Fatalf("root view missing %q:\n%s", want, view)
		}
	}
	for _, provider := range p.providers {
		if !strings.Contains(view, strings.ToUpper(provider.name)) {
			t.Fatalf("root view missing provider %q", provider.name)
		}
	}
}

func TestPickerCurrentModelInitialCursor(t *testing.T) {
	model := core.ListModels()[0].Model
	p := newPicker()
	p.Open(model.ID, nil)
	if row := p.root[p.cursor]; row.kind != pickerProvider || p.providers[row.provider].name != model.Provider {
		t.Fatalf("current unpinned model cursor = %+v, want provider %q", row, model.Provider)
	}
	p.Open(model.ID, map[string]bool{model.ID: true})
	if row := p.root[p.cursor]; row.kind != pickerPinnedModel || p.entries[row.entry].model.ID != model.ID {
		t.Fatalf("current pinned model cursor = %+v, want %q", row, model.ID)
	}
}

func TestPickerRestoresScopedIDs(t *testing.T) {
	model := core.ListModels()[0].Model
	p := newPicker()
	p.Open("", map[string]bool{model.ID: true, "custom-model": true})
	p.Close()
	p.Open("", p.ScopedIDs())
	ids := p.ScopedIDs()
	if !ids[model.ID] || !ids["custom-model"] {
		t.Fatalf("ScopedIDs = %v, want known and preserved custom IDs", ids)
	}
}

func TestPickerProviderDetailView(t *testing.T) {
	p := newPicker()
	p.Open("", nil)
	p.cursor = rootProviderCursor(t, p, 0)
	p.Activate()
	if view := p.View(100); !strings.Contains(view, "← All models") {
		t.Fatalf("provider view missing back heading:\n%s", view)
	}
}

func rootProviderCursor(t *testing.T, p pickerModel, provider int) int {
	t.Helper()
	for i, row := range p.root {
		if row.kind == pickerProvider && row.provider == provider {
			return i
		}
	}
	t.Fatalf("provider %d not found in root", provider)
	return 0
}

func TestCycleThinkingLevel(t *testing.T) {
	tests := []struct{ in, want string }{
		{"off", "low"}, {"low", "medium"}, {"medium", "high"},
		{"high", "xhigh"}, {"xhigh", "off"}, {"unknown", "medium"},
	}
	for _, tt := range tests {
		if got := cycleThinkingLevel(tt.in); got != tt.want {
			t.Errorf("cycleThinkingLevel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
