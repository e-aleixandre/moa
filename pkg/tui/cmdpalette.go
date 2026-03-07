package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// commandDef describes a slash command.
type commandDef struct {
	Name string
	Args string // e.g. "<spec>" or "" if no args
	Desc string
}

// allCommands is the full list of available commands, in display order.
var allCommands = []commandDef{
	{Name: "model", Args: "<spec>", Desc: "Switch model or open picker"},
	{Name: "thinking", Args: "<level>", Desc: "Set thinking level (off/minimal/low/medium/high)"},
	{Name: "compact", Desc: "Force context compaction"},
	{Name: "permissions", Args: "<mode>", Desc: "View or set permission mode (yolo/ask/auto)"},
	{Name: "clear", Desc: "Clear conversation and start fresh"},
	{Name: "exit", Desc: "Quit moa"},
}

// cmdPalette shows a filterable command list when the user types "/".
type cmdPalette struct {
	active   bool
	filter   string // text after "/" used to filter
	matches  []commandDef
	cursor   int
}

func (p *cmdPalette) Update(text string) {
	if !strings.HasPrefix(text, "/") {
		p.active = false
		return
	}

	filter := text[1:]

	// If there's a space, the user is typing args — close palette
	if strings.Contains(filter, " ") {
		p.active = false
		return
	}

	p.active = true
	p.filter = filter
	p.matches = filterCommands(filter)

	// Clamp cursor
	if p.cursor >= len(p.matches) {
		p.cursor = max(0, len(p.matches)-1)
	}
}

func (p *cmdPalette) MoveUp() {
	if p.cursor > 0 {
		p.cursor--
	}
}

func (p *cmdPalette) MoveDown() {
	if p.cursor < len(p.matches)-1 {
		p.cursor++
	}
}

// Selected returns the highlighted command name, or "" if nothing.
func (p *cmdPalette) Selected() string {
	if !p.active || len(p.matches) == 0 {
		return ""
	}
	return p.matches[p.cursor].Name
}

// SelectedHasArgs returns true if the highlighted command accepts args.
func (p *cmdPalette) SelectedHasArgs() bool {
	if !p.active || len(p.matches) == 0 {
		return false
	}
	return p.matches[p.cursor].Args != ""
}

func (p *cmdPalette) Close() {
	p.active = false
	p.filter = ""
	p.cursor = 0
}

func (p *cmdPalette) View(width int, theme Theme) string {
	if !p.active || len(p.matches) == 0 {
		return ""
	}

	dim := lipgloss.NewStyle().Foreground(theme.Overlay0)
	name := lipgloss.NewStyle().Foreground(theme.Mauve).Bold(true)
	args := lipgloss.NewStyle().Foreground(theme.Subtext0)
	desc := lipgloss.NewStyle().Foreground(theme.Subtext1)
	sel := lipgloss.NewStyle().Foreground(theme.Text).Bold(true)

	var lines []string
	for i, cmd := range p.matches {
		cursor := "  "
		if i == p.cursor {
			cursor = "▸ "
		}

		var line string
		if i == p.cursor {
			cmdStr := sel.Render("/" + cmd.Name)
			if cmd.Args != "" {
				cmdStr += " " + args.Render(cmd.Args)
			}
			line = fmt.Sprintf("%s%s  %s", cursor, cmdStr, desc.Render(cmd.Desc))
		} else {
			cmdStr := name.Render("/" + cmd.Name)
			if cmd.Args != "" {
				cmdStr += " " + dim.Render(cmd.Args)
			}
			line = fmt.Sprintf("%s%s  %s", cursor, cmdStr, dim.Render(cmd.Desc))
		}

		lines = append(lines, line)
	}

	content := strings.Join(lines, "\n")
	innerWidth := width - 4
	if innerWidth < 30 {
		innerWidth = 30
	}
	return pickerBorderStyle.Width(innerWidth).Render(content)
}

func filterCommands(filter string) []commandDef {
	if filter == "" {
		return allCommands
	}
	lower := strings.ToLower(filter)
	var result []commandDef
	for _, cmd := range allCommands {
		if strings.HasPrefix(cmd.Name, lower) {
			result = append(result, cmd)
		}
	}
	return result
}
