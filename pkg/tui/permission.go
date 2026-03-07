package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ealeixandre/moa/pkg/permission"
)

// permOption is one choice in the permission prompt.
type permOption struct {
	label    string // e.g. "Yes", "Yes, always allow Bash(git:*)", "No"
	approved bool
	allow    string // glob pattern to add (empty for plain yes/no)
}

// permissionPrompt replaces the input area with a numbered selector
// when tool permissions require user approval.
type permissionPrompt struct {
	active   bool
	request  permission.Request
	options  []permOption
	cursor   int
	amending bool   // Tab pressed: editing the selected option's text
	amendBuf string // text after ", "
}

func (p *permissionPrompt) Show(req permission.Request) {
	pattern := permission.GenerateAllowPattern(req.ToolName, req.Args)
	p.active = true
	p.request = req
	p.cursor = 0
	p.amending = false
	p.amendBuf = ""
	p.options = []permOption{
		{label: "Yes", approved: true},
		{label: fmt.Sprintf("Yes, always allow %s", pattern), approved: true, allow: pattern},
		{label: "No", approved: false},
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

	summary := permissionSummary(p.request.ToolName, p.request.Args)

	var lines []string

	// Header: "⚠ approve write?"
	lines = append(lines, warn.Render(fmt.Sprintf("  ⚠ approve %s?", p.request.ToolName)))

	// Tool summary (command, path, etc.)
	if summary != "" {
		maxW := width - 4
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
			if p.amending {
				text = sel.Render(opt.label+", ") + body.Render(p.amendBuf+"█")
			} else {
				text = sel.Render(opt.label)
			}
		} else {
			text = normal.Render(opt.label)
		}

		lines = append(lines, fmt.Sprintf("%s%s %s", cursor, numStr, text))
	}

	lines = append(lines, "")
	lines = append(lines, dim.Render("  Esc cancel · Tab amend"))

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
