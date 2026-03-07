package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ealeixandre/moa/pkg/permission"
)

// permissionPrompt shows a tool approval dialog when permissions require it.
type permissionPrompt struct {
	active   bool
	request  permission.Request
	ruleMode bool   // true when user is typing a rule
	ruleBuf  string // rule text being typed
}

func (p *permissionPrompt) Show(req permission.Request) {
	p.active = true
	p.request = req
	p.ruleMode = false
	p.ruleBuf = ""
}

func (p *permissionPrompt) Approve() {
	if p.active {
		p.request.Response <- true
		p.active = false
	}
}

func (p *permissionPrompt) Deny() {
	if p.active {
		p.request.Response <- false
		p.active = false
	}
}

// AllowPattern returns the glob pattern that would be added for "always allow".
func (p *permissionPrompt) AllowPattern() string {
	if !p.active {
		return ""
	}
	return permission.GenerateAllowPattern(p.request.ToolName, p.request.Args)
}

func (p *permissionPrompt) View(width int, theme Theme) string {
	if !p.active {
		return ""
	}

	warn := lipgloss.NewStyle().Foreground(theme.Yellow).Bold(true)
	dim := lipgloss.NewStyle().Foreground(theme.Overlay0)
	key := lipgloss.NewStyle().Foreground(theme.Mauve).Bold(true)
	body := lipgloss.NewStyle().Foreground(theme.Text)

	// Tool summary: most relevant arg
	summary := permissionSummary(p.request.ToolName, p.request.Args)

	var lines []string
	lines = append(lines, warn.Render(fmt.Sprintf("  ⚠ approve %s?", p.request.ToolName)))

	if summary != "" {
		maxW := width - 4
		if maxW > 0 && lipgloss.Width(summary) > maxW {
			summary = summary[:maxW-1] + "…"
		}
		lines = append(lines, body.Render("  "+summary))
	}

	if p.ruleMode {
		ruleLabel := "  " + dim.Render("rule: ") + body.Render(p.ruleBuf+"█")
		lines = append(lines, ruleLabel)
		hint := "  " + dim.Render("enter to save, esc to cancel")
		lines = append(lines, hint)
	} else {
		pattern := p.AllowPattern()
		keys := "  " +
			key.Render("[y]") + dim.Render(" approve  ") +
			key.Render("[n]") + dim.Render(" deny  ") +
			key.Render("[a]") + dim.Render(" always allow "+pattern+"  ") +
			key.Render("[r]") + dim.Render(" add rule")
		lines = append(lines, keys)
	}

	return strings.Join(lines, "\n")
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
	// Fallback: show all args compactly
	if len(args) == 0 {
		return ""
	}
	var parts []string
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, " ")
}
