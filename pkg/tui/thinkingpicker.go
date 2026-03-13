package tui

import "strings"

// thinkingPicker is a small inline selector for thinking levels.
type thinkingPicker struct {
	active  bool
	cursor  int
	entries []thinkingEntry
}

type thinkingEntry struct {
	label string
	value string // value to pass to the agent/provider
}

var defaultThinkingEntries = []thinkingEntry{
	{"None", "none"},
	{"Low", "low"},
	{"Medium", "medium"},
	{"High", "high"},
}

func (p *thinkingPicker) Open(currentLevel string) {
	p.entries = defaultThinkingEntries
	p.cursor = 0
	for i, e := range p.entries {
		if e.value == currentLevel {
			p.cursor = i
			break
		}
	}
	p.active = true
}

func (p *thinkingPicker) Close()    { p.active = false }
func (p *thinkingPicker) MoveUp()   { if p.cursor > 0 { p.cursor-- } }
func (p *thinkingPicker) MoveDown() { if p.cursor < len(p.entries)-1 { p.cursor++ } }

func (p *thinkingPicker) Selected() thinkingEntry {
	if p.cursor < len(p.entries) {
		return p.entries[p.cursor]
	}
	return thinkingEntry{}
}

func (p *thinkingPicker) View(width int, theme Theme) string {
	if !p.active || len(p.entries) == 0 {
		return ""
	}

	header := pickerHeaderStyle.Render("Thinking level — ↑↓ navigate · enter select · esc cancel")
	var lines []string
	lines = append(lines, header)
	lines = append(lines, "")

	for i, e := range p.entries {
		cursor := "  "
		if i == p.cursor {
			cursor = "▸ "
		}
		text := cursor + e.label
		if i == p.cursor {
			lines = append(lines, pickerSelectedStyle.Render(text))
		} else {
			lines = append(lines, text)
		}
	}

	content := strings.Join(lines, "\n")
	innerWidth := width - 4
	if innerWidth < 30 {
		innerWidth = 30
	}
	return pickerBorderStyle.Width(innerWidth).Render(content)
}
