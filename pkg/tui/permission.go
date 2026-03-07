package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ealeixandre/moa/pkg/permission"
)

// permOption is one choice in the permission prompt.
type permOption struct {
	label    string
	approved bool
	allow    string // ask mode: glob pattern to add
	addRule  bool   // auto mode: option 3 triggers rule input
}

// permissionPrompt replaces the input area with a numbered selector
// when tool permissions require user approval.
type permissionPrompt struct {
	active   bool
	mode     permission.Mode
	request  permission.Request
	options  []permOption
	cursor   int
	amending bool   // Tab: editing feedback after the selected option
	amendBuf string // text after ", "
	ruleMode bool   // auto mode: typing a rule in option 3
	ruleBuf  string
}

func (p *permissionPrompt) Show(req permission.Request, mode permission.Mode) {
	p.active = true
	p.mode = mode
	p.request = req
	p.cursor = 0
	p.amending = false
	p.amendBuf = ""
	p.ruleMode = false
	p.ruleBuf = ""

	switch mode {
	case permission.ModeAuto:
		p.options = []permOption{
			{label: "Yes", approved: true},
			{label: "No", approved: false},
			{label: "Add rule", addRule: true},
		}
	default: // ModeAsk
		pattern := permission.GenerateAllowPattern(req.ToolName, req.Args)
		p.options = []permOption{
			{label: "Yes", approved: true},
			{label: fmt.Sprintf("Yes, always allow %s", pattern), approved: true, allow: pattern},
			{label: "No", approved: false},
		}
	}
}

func (p *permissionPrompt) respond(resp permission.Response) {
	if p.active {
		p.request.Response <- resp
		p.active = false
	}
}

func (p *permissionPrompt) Confirm() {
	if !p.active || p.cursor >= len(p.options) {
		return
	}
	opt := p.options[p.cursor]
	p.respond(permission.Response{
		Approved: opt.approved,
		Feedback: strings.TrimSpace(p.amendBuf),
		Allow:    opt.allow,
	})
}

func (p *permissionPrompt) Cancel() {
	p.respond(permission.Response{Approved: false})
}

// SaveRule stores the typed rule and stays on the prompt.
// Returns the rule text (caller adds it to the gate).
func (p *permissionPrompt) SaveRule() string {
	rule := strings.TrimSpace(p.ruleBuf)
	p.ruleMode = false
	p.ruleBuf = ""
	p.cursor = 0 // back to Yes
	return rule
}

func (p *permissionPrompt) View(width int, theme Theme) string {
	if !p.active {
		return ""
	}

	warn := lipgloss.NewStyle().Foreground(theme.Yellow).Bold(true)
	dim := lipgloss.NewStyle().Foreground(theme.Overlay0)
	num := lipgloss.NewStyle().Foreground(theme.Overlay1)
	sel := lipgloss.NewStyle().Foreground(theme.Text).Bold(true)
	normal := lipgloss.NewStyle().Foreground(theme.Subtext1)
	body := lipgloss.NewStyle().Foreground(theme.Text)
	green := lipgloss.NewStyle().Foreground(theme.Green)

	summary := permissionSummary(p.request.ToolName, p.request.Args)

	var lines []string

	// Header
	lines = append(lines, warn.Render(fmt.Sprintf("  ⚠ approve %s?", p.request.ToolName)))

	// Tool summary
	if summary != "" {
		maxW := width - 6
		if maxW > 0 && lipgloss.Width(summary) > maxW {
			summary = summary[:maxW-1] + "…"
		}
		lines = append(lines, body.Render("  "+summary))
	}

	lines = append(lines, "")

	// Options
	for i, opt := range p.options {
		cursor := "  "
		if i == p.cursor {
			cursor = "▸ "
		}

		numStr := num.Render(fmt.Sprintf("%d.", i+1))

		var text string
		if i == p.cursor {
			if opt.addRule && p.ruleMode {
				text = sel.Render(opt.label+": ") + body.Render(p.ruleBuf+"█")
			} else if p.amending {
				text = sel.Render(opt.label+", ") + body.Render(p.amendBuf+"█")
			} else {
				text = sel.Render(opt.label)
			}
		} else {
			text = normal.Render(opt.label)
		}

		lines = append(lines, fmt.Sprintf("%s%s %s", cursor, numStr, text))
	}

	// Show saved rules count if any exist in auto mode
	if p.mode == permission.ModeAuto {
		lines = append(lines, "")
		if p.ruleMode {
			lines = append(lines, dim.Render("  Enter save · Esc cancel"))
		} else {
			hint := "  Esc cancel · Tab amend"
			lines = append(lines, dim.Render(hint))
		}
	} else {
		lines = append(lines, "")
		lines = append(lines, dim.Render("  Esc cancel · Tab amend"))
	}

	// Show status after saving a rule
	_ = green // used by caller status blocks

	content := strings.Join(lines, "\n")
	innerWidth := width - 4
	if innerWidth < 30 {
		innerWidth = 30
	}
	return pickerBorderStyle.Width(innerWidth).Render(content)
}

// permissionSummary extracts the most relevant arg for display.
func permissionSummary(toolName string, args map[string]any) string {
	switch toolName {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	case "write", "edit", "read":
		if path, ok := args["path"].(string); ok {
			return path
		}
	}
	if len(args) == 0 {
		return ""
	}
	var parts []string
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, " ")
}
