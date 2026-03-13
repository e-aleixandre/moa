package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// settingsEntry represents one row in the settings menu.
type settingsEntry struct {
	key     string   // display key, e.g. "Permissions"
	value   string   // current value, e.g. "yolo"
	options []string // cycle options (empty = display-only)
}

// settingsMenu is an overlay that shows and lets the user change session settings.
type settingsMenu struct {
	active  bool
	cursor  int
	entries []settingsEntry
}

func (s *settingsMenu) Open(entries []settingsEntry) {
	s.active = true
	s.cursor = 0
	s.entries = entries
}

func (s *settingsMenu) Close() {
	s.active = false
	s.cursor = 0
	s.entries = nil
}

func (s *settingsMenu) MoveUp() {
	if s.cursor > 0 {
		s.cursor--
	}
}

func (s *settingsMenu) MoveDown() {
	if s.cursor < len(s.entries)-1 {
		s.cursor++
	}
}

// CycleValue advances the current entry to the next option. Returns the
// key and new value, or empty strings if the entry has no options.
func (s *settingsMenu) CycleValue() (key, newValue string) {
	if !s.active || s.cursor >= len(s.entries) {
		return "", ""
	}
	e := &s.entries[s.cursor]
	if len(e.options) == 0 {
		return "", ""
	}
	idx := 0
	for i, opt := range e.options {
		if opt == e.value {
			idx = (i + 1) % len(e.options)
			break
		}
	}
	e.value = e.options[idx]
	return e.key, e.value
}

// Selected returns the key of the current entry (for Enter handling).
func (s *settingsMenu) Selected() string {
	if !s.active || s.cursor >= len(s.entries) {
		return ""
	}
	return s.entries[s.cursor].key
}

func (s *settingsMenu) View(width int, theme Theme) string {
	if !s.active || len(s.entries) == 0 {
		return ""
	}

	title := lipgloss.NewStyle().Foreground(theme.Mauve).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(theme.Overlay0)
	selStyle := lipgloss.NewStyle().Foreground(theme.Text).Bold(true)
	valStyle := lipgloss.NewStyle().Foreground(theme.Blue)
	keyStyle := lipgloss.NewStyle().Foreground(theme.Subtext1)

	// Find max key width for alignment.
	maxKey := 0
	for _, e := range s.entries {
		if len(e.key) > maxKey {
			maxKey = len(e.key)
		}
	}

	var lines []string
	lines = append(lines, title.Render("⚙  Settings"))
	lines = append(lines, "")

	for i, e := range s.entries {
		cursor := "  "
		if i == s.cursor {
			cursor = "▸ "
		}
		padded := e.key + strings.Repeat(" ", maxKey-len(e.key))
		if i == s.cursor {
			line := selStyle.Render(cursor+padded+"  ") + valStyle.Render(e.value)
			if len(e.options) > 0 {
				line += dimStyle.Render("  ←/→")
			}
			lines = append(lines, line)
		} else {
			lines = append(lines, dimStyle.Render(cursor)+keyStyle.Render(padded+"  ")+valStyle.Render(e.value))
		}
	}

	lines = append(lines, "")
	lines = append(lines, dimStyle.Render("  ↑↓ navigate  ←→/Enter cycle  Esc close"))

	content := strings.Join(lines, "\n")
	innerWidth := width - 4
	if innerWidth < 40 {
		innerWidth = 40
	}
	return pickerBorderStyle.Width(innerWidth).Render(content)
}
