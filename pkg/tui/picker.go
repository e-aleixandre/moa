package tui

import (
	"fmt"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

type pickerLevel int

const (
	pickerRoot pickerLevel = iota
	pickerProviderDetail
)

type pickerRootEntryKind int

const (
	pickerPinnedModel pickerRootEntryKind = iota
	pickerProvider
)

// pickerModel is an inline model picker with a provider overview and a model
// detail level.
type pickerModel struct {
	active bool

	entries   []pickerEntry
	providers []pickerProviderEntry
	root      []pickerRootEntry
	scoped    map[string]bool

	level          pickerLevel
	cursor         int
	rootCursor     int
	detailProvider int
}

type pickerEntry struct {
	model   core.Model
	alias   string
	scoped  bool // pinned for Ctrl+P cycling
	current bool // currently active model
}

type pickerProviderEntry struct {
	name    string
	entries []int
}

type pickerRootEntry struct {
	kind     pickerRootEntryKind
	entry    int
	provider int
}

// pickerBorderStyle, pickerSelectedStyle, pickerScopedStyle, pickerDimStyle,
// pickerHeaderStyle are theme-derived. Rebuilt by RebuildUI() in styles.go.

func newPicker() pickerModel {
	return pickerModel{detailProvider: -1}
}

// Open populates the picker with known models and marks the current one.
func (p *pickerModel) Open(currentModelID string, scopedIDs map[string]bool) {
	models := core.ListModels()
	p.entries = make([]pickerEntry, len(models))
	p.providers = nil
	p.scoped = make(map[string]bool, len(scopedIDs))
	for id, scoped := range scopedIDs {
		if scoped {
			p.scoped[id] = true
		}
	}

	currentEntry := -1
	providerIndexes := make(map[string]int)
	for i, m := range models {
		isCurrent := m.Model.ID == currentModelID
		p.entries[i] = pickerEntry{
			model:   m.Model,
			alias:   m.Alias,
			scoped:  p.scoped[m.Model.ID],
			current: isCurrent,
		}
		if isCurrent {
			currentEntry = i
		}

		provider, ok := providerIndexes[m.Model.Provider]
		if !ok {
			provider = len(p.providers)
			providerIndexes[m.Model.Provider] = provider
			p.providers = append(p.providers, pickerProviderEntry{name: m.Model.Provider})
		}
		p.providers[provider].entries = append(p.providers[provider].entries, i)
	}

	p.level = pickerRoot
	p.detailProvider = -1
	p.cursor = 0
	p.rootCursor = 0
	p.rebuildRoot()
	p.setInitialCursor(currentEntry)
	p.active = true
}

func (p *pickerModel) rebuildRoot() {
	p.root = p.root[:0]
	for i, entry := range p.entries {
		if entry.scoped {
			p.root = append(p.root, pickerRootEntry{kind: pickerPinnedModel, entry: i})
		}
	}
	for i := range p.providers {
		p.root = append(p.root, pickerRootEntry{kind: pickerProvider, provider: i})
	}
}

func (p *pickerModel) setInitialCursor(currentEntry int) {
	if currentEntry < 0 {
		return
	}
	if p.entries[currentEntry].scoped {
		for i, entry := range p.root {
			if entry.kind == pickerPinnedModel && entry.entry == currentEntry {
				p.cursor, p.rootCursor = i, i
				return
			}
		}
	}
	currentProvider := p.entries[currentEntry].model.Provider
	for i, entry := range p.root {
		if entry.kind == pickerProvider && p.providers[entry.provider].name == currentProvider {
			p.cursor, p.rootCursor = i, i
			return
		}
	}
}

func (p *pickerModel) Close() { p.active = false }

func (p *pickerModel) MoveUp() {
	if p.cursor > 0 {
		p.cursor--
	}
	if p.level == pickerRoot {
		p.rootCursor = p.cursor
	}
}

func (p *pickerModel) MoveDown() {
	if p.cursor < p.rowCount()-1 {
		p.cursor++
	}
	if p.level == pickerRoot {
		p.rootCursor = p.cursor
	}
}

func (p *pickerModel) rowCount() int {
	if p.level == pickerProviderDetail && p.detailProvider >= 0 && p.detailProvider < len(p.providers) {
		return len(p.providers[p.detailProvider].entries)
	}
	return len(p.root)
}

// Activate selects a model when one is highlighted. On a root provider row it
// opens that provider and returns false.
func (p *pickerModel) Activate() (core.Model, bool) {
	if p.level == pickerRoot {
		if p.cursor < 0 || p.cursor >= len(p.root) {
			return core.Model{}, false
		}
		row := p.root[p.cursor]
		if row.kind == pickerProvider {
			p.openProvider(row.provider)
			return core.Model{}, false
		}
		return p.entries[row.entry].model, true
	}
	if p.detailProvider < 0 || p.detailProvider >= len(p.providers) {
		return core.Model{}, false
	}
	entries := p.providers[p.detailProvider].entries
	if p.cursor < 0 || p.cursor >= len(entries) {
		return core.Model{}, false
	}
	return p.entries[entries[p.cursor]].model, true
}

func (p *pickerModel) openProvider(provider int) {
	if provider < 0 || provider >= len(p.providers) {
		return
	}
	p.rootCursor = p.cursor
	p.level = pickerProviderDetail
	p.detailProvider = provider
	p.cursor = 0
	for i, entry := range p.providers[provider].entries {
		if p.entries[entry].current {
			p.cursor = i
			break
		}
	}
}

// Back returns to the provider overview and reports whether it did so.
func (p *pickerModel) Back() bool {
	if p.level != pickerProviderDetail {
		return false
	}
	provider := p.detailProvider
	p.level = pickerRoot
	p.detailProvider = -1
	p.rebuildRoot()
	p.cursor = p.providerRootCursor(provider)
	p.rootCursor = p.cursor
	p.clampCursor()
	return true
}

func (p *pickerModel) providerRootCursor(provider int) int {
	for i, row := range p.root {
		if row.kind == pickerProvider && row.provider == provider {
			return i
		}
	}
	return p.rootCursor
}

// ToggleScoped toggles the pinned status of the highlighted model. Provider
// rows in the root view are intentionally not pinnable.
func (p *pickerModel) ToggleScoped() {
	entry, ok := p.selectedEntry()
	if !ok {
		return
	}
	p.entries[entry].scoped = !p.entries[entry].scoped
	if p.entries[entry].scoped {
		p.scoped[p.entries[entry].model.ID] = true
	} else {
		delete(p.scoped, p.entries[entry].model.ID)
	}
	p.rebuildRoot()
	if p.level == pickerRoot {
		p.clampCursor()
		p.rootCursor = p.cursor
	}
}

func (p *pickerModel) selectedEntry() (int, bool) {
	if p.level == pickerRoot {
		if p.cursor < 0 || p.cursor >= len(p.root) || p.root[p.cursor].kind != pickerPinnedModel {
			return 0, false
		}
		return p.root[p.cursor].entry, true
	}
	if p.detailProvider < 0 || p.detailProvider >= len(p.providers) {
		return 0, false
	}
	entries := p.providers[p.detailProvider].entries
	if p.cursor < 0 || p.cursor >= len(entries) {
		return 0, false
	}
	return entries[p.cursor], true
}

func (p *pickerModel) clampCursor() {
	if p.cursor < 0 {
		p.cursor = 0
	}
	if rows := p.rowCount(); rows == 0 {
		p.cursor = 0
	} else if p.cursor >= rows {
		p.cursor = rows - 1
	}
}

// Selected returns the highlighted model, or a zero Model if the highlighted
// row is a provider or the cursor is out of range.
func (p *pickerModel) Selected() core.Model {
	entry, ok := p.selectedEntry()
	if !ok {
		return core.Model{}
	}
	return p.entries[entry].model
}

// ScopedIDs returns the set of scoped model IDs.
func (p *pickerModel) ScopedIDs() map[string]bool {
	result := make(map[string]bool, len(p.scoped))
	for id := range p.scoped {
		result[id] = true
	}
	return result
}

// View renders the picker list.
func (p pickerModel) View(width int) string {
	if !p.active || len(p.entries) == 0 {
		return ""
	}
	var lines []string
	if p.level == pickerProviderDetail {
		lines = p.providerViewLines()
	} else {
		lines = p.rootViewLines()
	}
	content := strings.Join(lines, "\n")
	innerWidth := width - 4
	if innerWidth < 30 {
		innerWidth = 30
	}
	return pickerBorderStyle.Width(innerWidth).Render(content)
}

func (p pickerModel) rootViewLines() []string {
	lines := []string{
		pickerHeaderStyle.Render("Models — ↑↓ navigate · enter open/select · space pin · esc close"),
		"",
		pickerDimStyle.Render("  PINNED"),
	}
	pinned := 0
	for i, row := range p.root {
		if row.kind != pickerPinnedModel {
			continue
		}
		pinned++
		lines = append(lines, p.renderModelEntry(p.entries[row.entry], i == p.cursor))
	}
	if pinned == 0 {
		lines = append(lines, pickerDimStyle.Render("  No pinned models — open a provider and press space to pin"))
	}
	lines = append(lines, "", pickerDimStyle.Render("  ALL MODELS"))
	for i, row := range p.root {
		if row.kind == pickerProvider {
			lines = append(lines, p.renderProviderEntry(row.provider, i == p.cursor))
		}
	}
	return lines
}

func (p pickerModel) providerViewLines() []string {
	if p.detailProvider < 0 || p.detailProvider >= len(p.providers) {
		return nil
	}
	provider := p.providers[p.detailProvider]
	lines := []string{
		pickerHeaderStyle.Render("Models — " + strings.ToUpper(provider.name) + " · ↑↓ navigate · enter select · space pin · esc back"),
		"",
		pickerDimStyle.Render("  ← All models"),
		pickerDimStyle.Render("  " + strings.ToUpper(provider.name)),
	}
	for i, entry := range provider.entries {
		lines = append(lines, p.renderModelEntry(p.entries[entry], i == p.cursor))
	}
	return lines
}

func (p pickerModel) renderProviderEntry(providerIndex int, selected bool) string {
	provider := p.providers[providerIndex]
	preview := make([]string, 0, 3)
	current := false
	for _, entry := range provider.entries {
		if len(preview) < 3 {
			preview = append(preview, modelName(p.entries[entry]))
		}
		current = current || p.entries[entry].current
	}
	cursor := "  "
	if selected {
		cursor = "▸ "
	}
	count := "models"
	if len(provider.entries) == 1 {
		count = "model"
	}
	currentMark := ""
	if current {
		currentMark = " ✓"
	}
	text := fmt.Sprintf("%s%s — %d %s · %s%s", cursor, strings.ToUpper(provider.name), len(provider.entries), count, strings.Join(preview, ", "), currentMark)
	if selected {
		return pickerSelectedStyle.Render(text)
	}
	return text
}

func (p pickerModel) renderModelEntry(entry pickerEntry, selected bool) string {
	cursor := "  "
	if selected {
		cursor = "▸ "
	}
	pin := "  "
	if entry.scoped {
		pin = "● "
	}
	alias := ""
	if entry.alias != "" {
		alias = fmt.Sprintf(" (%s)", entry.alias)
	}
	current := ""
	if entry.current {
		current = " ✓"
	}
	text := fmt.Sprintf("%s%s%s%s%s", cursor, pin, modelName(entry), alias, current)
	if selected {
		return pickerSelectedStyle.Render(text)
	}
	if entry.scoped {
		return pickerScopedStyle.Render(text)
	}
	return text
}

func modelName(entry pickerEntry) string {
	if entry.model.Name != "" {
		return entry.model.Name
	}
	return entry.model.ID
}
