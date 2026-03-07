package tui

import (
	"fmt"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// pickerModel is an inline model picker. It renders as part of View()
// and intercepts keys when active. Supports:
//   - Up/Down (or j/k) to navigate
//   - Enter to select the highlighted model and close
//   - Space to toggle scoped (pinned) status
//   - Escape to close without changing
type pickerModel struct {
	active  bool
	entries []pickerEntry
	cursor  int
}

type pickerEntry struct {
	model   core.Model
	alias   string
	scoped  bool // pinned for Ctrl+P cycling
	current bool // currently active model
}

// pickerBorderStyle, pickerSelectedStyle, pickerScopedStyle, pickerDimStyle,
// pickerHeaderStyle are theme-derived. Rebuilt by RebuildUI() in styles.go.

func newPicker() pickerModel {
	return pickerModel{}
}

// Open populates the picker with known models and marks the current one.
func (p *pickerModel) Open(currentModelID string, scopedIDs map[string]bool) {
	models := core.ListModels()
	p.entries = make([]pickerEntry, len(models))
	p.cursor = 0

	for i, m := range models {
		isCurrent := m.Model.ID == currentModelID
		p.entries[i] = pickerEntry{
			model:   m.Model,
			alias:   m.Alias,
			scoped:  scopedIDs[m.Model.ID],
			current: isCurrent,
		}
		if isCurrent {
			p.cursor = i
		}
	}
	p.active = true
}

func (p *pickerModel) Close() {
	p.active = false
}

func (p *pickerModel) MoveUp() {
	if p.cursor > 0 {
		p.cursor--
	}
}

func (p *pickerModel) MoveDown() {
	if p.cursor < len(p.entries)-1 {
		p.cursor++
	}
}

// ToggleScoped toggles the scoped status of the highlighted model.
func (p *pickerModel) ToggleScoped() {
	if p.cursor >= 0 && p.cursor < len(p.entries) {
		p.entries[p.cursor].scoped = !p.entries[p.cursor].scoped
	}
}

// Selected returns the highlighted model.
func (p *pickerModel) Selected() core.Model {
	return p.entries[p.cursor].model
}

// ScopedIDs returns the set of scoped model IDs.
func (p *pickerModel) ScopedIDs() map[string]bool {
	result := make(map[string]bool)
	for _, e := range p.entries {
		if e.scoped {
			result[e.model.ID] = true
		}
	}
	return result
}

// View renders the picker list.
func (p pickerModel) View(width int) string {
	if !p.active || len(p.entries) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, pickerHeaderStyle.Render("Models — ↑↓ navigate · enter select · space pin · esc close"))
	lines = append(lines, "")

	lastProvider := ""
	for i, e := range p.entries {
		// Provider header.
		if e.model.Provider != lastProvider {
			lastProvider = e.model.Provider
			lines = append(lines, pickerDimStyle.Render("  "+strings.ToUpper(e.model.Provider)))
		}

		line := p.renderEntry(i, e)
		lines = append(lines, line)
	}

	content := strings.Join(lines, "\n")
	innerWidth := width - 4
	if innerWidth < 30 {
		innerWidth = 30
	}
	return pickerBorderStyle.Width(innerWidth).Render(content)
}

func (p pickerModel) renderEntry(idx int, e pickerEntry) string {
	// Cursor indicator.
	cursor := "  "
	if idx == p.cursor {
		cursor = "▸ "
	}

	// Scoped indicator.
	pin := "  "
	if e.scoped {
		pin = "● "
	}

	// Model name.
	name := e.model.Name
	if name == "" {
		name = e.model.ID
	}

	// Alias hint.
	alias := ""
	if e.alias != "" {
		alias = fmt.Sprintf(" (%s)", e.alias)
	}

	// Current marker.
	current := ""
	if e.current {
		current = " ✓"
	}

	text := fmt.Sprintf("%s%s%s%s%s", cursor, pin, name, alias, current)

	if idx == p.cursor {
		return pickerSelectedStyle.Render(text)
	}
	if e.scoped {
		return pickerScopedStyle.Render(text)
	}
	return text
}
