package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ealeixandre/moa/pkg/permission"
)

// permissionPrompt shows a tool approval dialog when permissions require it.
type permissionPrompt struct {
	active  bool
	request permission.Request
}

func (p *permissionPrompt) Show(req permission.Request) {
	p.active = true
	p.request = req
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

	// Render: ⚠ approve bash?
	//         git status && npm test
	//         [y] approve  [n] deny
	var lines []string
	lines = append(lines, warn.Render(fmt.Sprintf("  ⚠ approve %s?", p.request.ToolName)))

	if summary != "" {
		// Truncate long summaries
		maxW := width - 4
		if maxW > 0 && lipgloss.Width(summary) > maxW {
			summary = summary[:maxW-1] + "…"
		}
		lines = append(lines, body.Render("  "+summary))
	}

	keys := "  " +
		key.Render("[y]") + dim.Render(" approve  ") +
		key.Render("[n]") + dim.Render(" deny")
	lines = append(lines, keys)

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
