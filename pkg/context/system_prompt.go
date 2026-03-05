package agentcontext

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ealeixandre/go-agent/pkg/core"
)

// BuildSystemPrompt constructs the system prompt from components.
func BuildSystemPrompt(agentsMD string, tools []core.ToolSpec) string {
	var sb strings.Builder

	// Role
	sb.WriteString(`You are an expert coding agent. You help users by reading files, executing commands, editing code, and writing new files.

`)

	// Guidelines
	sb.WriteString(`Guidelines:
- Use bash for file operations like ls, rg, find
- Use read to examine files before editing
- Use edit for precise changes (oldText must match exactly)
- Use write only for new files or complete rewrites
- Be concise in your responses
- Show file paths clearly when working with files

`)

	// Available tools
	if len(tools) > 0 {
		sb.WriteString("Available tools:\n")
		for _, t := range tools {
			desc := t.Description
			if len(desc) > 200 {
				desc = desc[:197] + "..."
			}
			sb.WriteString(fmt.Sprintf("- %s: %s\n", t.Name, desc))
		}
		sb.WriteString("\n")
	}

	// AGENTS.md content
	if agentsMD != "" {
		sb.WriteString("# Project Context\n\n")
		sb.WriteString("Project-specific instructions and guidelines:\n\n")
		sb.WriteString(agentsMD)
		sb.WriteString("\n\n")
	}

	// Current date/time and working directory
	cwd, _ := os.Getwd()
	sb.WriteString(fmt.Sprintf("Current date and time: %s\n", time.Now().Format("Monday, January 2, 2006 at 3:04:05 PM MST")))
	sb.WriteString(fmt.Sprintf("Current working directory: %s\n", cwd))

	return sb.String()
}
