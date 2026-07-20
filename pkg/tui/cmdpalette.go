package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ealeixandre/moa/pkg/goal"
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
	{Name: "thinking", Args: "<level>", Desc: "Set thinking level (off/low/medium/high/xhigh)"},
	{Name: "compact", Desc: "Force context compaction"},
	{Name: "prepare-compact", Desc: "Prepare handoff then compact context"},
	{Name: "plan", Desc: "Toggle plan mode (plan-then-execute workflow)"},
	{Name: "goal", Args: "<objective> [flags]|stop|status", Desc: "Autonomous maker→verifier loop toward an objective"},
	{Name: "permissions", Args: "<mode>", Desc: "View or set permission mode (yolo/ask/auto)"},
	{Name: "path", Args: "[list|add|rm|scope]", Desc: "View or manage path access scope"},
	{Name: "tasks", Args: "[done <n> | reset | show]", Desc: "View/manage tasks"},
	{Name: "undo", Desc: "Revert files changed in the last agent turn"},
	{Name: "branch", Desc: "Rewind to an earlier point and start a new conversation branch"},
	{Name: "verify", Desc: "Run project verification checks"},
	{Name: "prompt", Args: "<name>", Desc: "Insert a prompt template"},
	{Name: "rename", Args: "<new title>", Desc: "Rename this session"},
	{Name: "voice", Desc: "Toggle voice recording (Ctrl+R)"},
	{Name: "settings", Desc: "Open settings menu"},
	{Name: "clear", Desc: "Clear conversation and start fresh"},
	{Name: "exit", Desc: "Quit moa"},
}

const paletteMaxVisible = 6 // fixed visible height

// cmdPalette shows a filterable command list when the user types "/".
type cmdPalette struct {
	active    bool
	filter    string // text after "/" used to filter
	matches   []commandDef
	cursor    int
	scroll    int  // first visible index
	flagMode  bool // true when suggesting "/goal" flags instead of commands
	flagToken string
}

func (p *cmdPalette) Update(text string) {
	// "/goal " with a trailing "-..." token switches to flag-suggestion mode.
	if strings.HasPrefix(text, "/goal ") {
		lastSpace := strings.LastIndex(text, " ")
		lastToken := text[lastSpace+1:]
		if strings.HasPrefix(lastToken, "-") {
			p.active = true
			p.flagMode = true
			p.flagToken = lastToken
			p.filter = lastToken
			p.matches = filterFlags(text, lastToken)
			p.scroll = 0
			if p.cursor >= len(p.matches) {
				p.cursor = max(0, len(p.matches)-1)
			}
			return
		}
	}

	if !strings.HasPrefix(text, "/") {
		p.active = false
		p.flagMode = false
		p.flagToken = ""
		return
	}

	filter := text[1:]

	// If there's a space, the user is typing args — close palette
	if strings.Contains(filter, " ") {
		p.active = false
		p.flagMode = false
		p.flagToken = ""
		return
	}

	p.active = true
	p.flagMode = false
	p.flagToken = ""
	p.filter = filter
	p.matches = filterCommands(filter)
	p.scroll = 0

	// Clamp cursor
	if p.cursor >= len(p.matches) {
		p.cursor = max(0, len(p.matches)-1)
	}
}

func (p *cmdPalette) MoveUp() {
	if p.cursor > 0 {
		p.cursor--
		if p.cursor < p.scroll {
			p.scroll = p.cursor
		}
	}
}

func (p *cmdPalette) MoveDown() {
	if p.cursor < len(p.matches)-1 {
		p.cursor++
		if p.cursor >= p.scroll+paletteMaxVisible {
			p.scroll = p.cursor - paletteMaxVisible + 1
		}
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

// FlagMode reports whether the palette is currently suggesting /goal flags
// rather than slash commands.
func (p *cmdPalette) FlagMode() bool {
	return p.flagMode
}

func (p *cmdPalette) Close() {
	p.active = false
	p.filter = ""
	p.cursor = 0
	p.flagMode = false
	p.flagToken = ""
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

	// Windowed view: only show paletteMaxVisible items
	end := p.scroll + paletteMaxVisible
	if end > len(p.matches) {
		end = len(p.matches)
	}
	visible := p.matches[p.scroll:end]

	var lines []string
	for vi, cmd := range visible {
		i := p.scroll + vi
		cursor := "  "
		if i == p.cursor {
			cursor = "▸ "
		}

		// Flag suggestions already carry their own "--" prefix.
		displayName := cmd.Name
		if !p.flagMode {
			displayName = "/" + cmd.Name
		}

		var line string
		if i == p.cursor {
			cmdStr := sel.Render(displayName)
			if cmd.Args != "" {
				cmdStr += " " + args.Render(cmd.Args)
			}
			line = fmt.Sprintf("%s%s  %s", cursor, cmdStr, desc.Render(cmd.Desc))
		} else {
			cmdStr := name.Render(displayName)
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

// filterFlags returns /goal flags whose name matches the token being typed
// (a "-" or "--" prefix), excluding flags already present as a complete
// token elsewhere in text.
func filterFlags(text, token string) []commandDef {
	used := make(map[string]bool)
	for _, tok := range strings.Fields(text) {
		if tok != token {
			used[tok] = true
		}
	}

	var result []commandDef
	for _, f := range goal.Flags() {
		if used[f.Name] {
			continue
		}
		if !strings.HasPrefix(f.Name, token) {
			continue
		}
		result = append(result, commandDef{Name: f.Name, Args: f.Placeholder, Desc: f.Desc})
	}
	return result
}

// replaceLastToken swaps the final whitespace-delimited token of value with
// replacement (used to insert a completed flag in place of what the user was
// typing).
func replaceLastToken(value, replacement string) string {
	idx := strings.LastIndex(value, " ")
	if idx < 0 {
		return replacement
	}
	return value[:idx+1] + replacement
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
