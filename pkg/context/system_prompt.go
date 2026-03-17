package agentcontext

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/git"
)

// toolSnippets provides concise one-line descriptions for built-in tools.
// These are used in the system prompt instead of the full JSON schema descriptions.
var toolSnippets = map[string]string{
	"read":            "Read the contents of a file. Supports text files and images.",
	"bash":            "Execute a bash command. Returns stdout, stderr, and exit code.",
	"edit":            "Make surgical edits to files (find exact text and replace).",
	"write":           "Create or overwrite files. Automatically creates parent directories.",
	"grep":            "Search file contents for patterns (respects .gitignore).",
	"find":            "Find files by glob pattern (respects .gitignore).",
	"ls":              "List directory contents. Sorted alphabetically. Directories have '/' suffix.",
	"subagent":        "Spawn a child agent with its own context for parallel investigation or focused subtasks.",
	"subagent_status": "Check the status of an async subagent job.",
	"subagent_cancel": "Cancel a running async subagent job.",
	"verify":          "Run project verification checks (build, test, lint) from .moa/verify.json.",
}

// BuildSystemPrompt constructs the system prompt from components.
// cwd is the working directory shown to the agent; if empty, os.Getwd() is used.
// hasVerify indicates a valid .moa/verify.json was loaded — adds a guideline
// instructing the agent to verify after coding tasks.
// skillsIndex is an optional pre-formatted index of available skills (empty = no skills).
func BuildSystemPrompt(agentsMD string, tools []core.ToolSpec, cwd string, hasVerify bool, skillsIndex ...string) string {
	var sb strings.Builder

	// Identity and role
	sb.WriteString(`You are Moa, an expert coding agent. You help users by reading files, executing commands, editing code, and writing new files.

`)

	// Available tools — concise snippets, not full descriptions
	if len(tools) > 0 {
		sb.WriteString("Available tools:\n")
		for _, t := range tools {
			desc := toolSnippets[t.Name]
			if desc == "" {
				// Custom/unknown tool — use original description, truncated
				desc = t.Description
				if len(desc) > 200 {
					desc = desc[:197] + "..."
				}
			}
			fmt.Fprintf(&sb, "- %s: %s\n", t.Name, desc)
		}
		sb.WriteString("\n")
	}

	// Build adaptive guidelines based on which tools are available
	toolSet := make(map[string]bool, len(tools))
	for _, t := range tools {
		toolSet[t.Name] = true
	}

	sb.WriteString("Guidelines:\n")

	// File exploration
	if toolSet["bash"] && (toolSet["grep"] || toolSet["find"] || toolSet["ls"]) {
		sb.WriteString("- Prefer grep/find/ls tools over bash for file exploration (faster, respects .gitignore)\n")
	} else if toolSet["bash"] {
		sb.WriteString("- Use bash for file operations like ls, rg, find\n")
	}

	// Read before edit
	if toolSet["read"] && toolSet["edit"] {
		sb.WriteString("- Use read to examine files before editing. Do not use cat or sed for this.\n")
	}

	// Edit
	if toolSet["edit"] {
		sb.WriteString("- Use edit for precise changes (old text must match exactly)\n")
	}

	// Write
	if toolSet["write"] {
		sb.WriteString("- Use write only for new files or complete rewrites\n")
	}

	// Output behavior
	if toolSet["edit"] || toolSet["write"] {
		sb.WriteString("- When summarizing your actions, output plain text directly — do NOT use cat or bash to display what you did\n")
	}

	// Verify
	if hasVerify && toolSet["verify"] {
		sb.WriteString("- After completing coding tasks, call the verify tool to validate your changes before reporting done\n")
	}

	// Subagent guidance
	if toolSet["subagent"] {
		sb.WriteString(`- Use subagents for tasks that benefit from a separate context window:
  • Systematic changes across many files (e.g. renaming imports, updating API calls)
  • Investigating how a feature works (exploring code without polluting your context)
  • Parallel independent tasks (use async=true)
  Each subagent gets a fresh context, so your own window stays focused on the main task.
`)
	}

	// Always include these
	sb.WriteString(`- Be concise in your responses
- Show file paths clearly when working with files

`)

	// AGENTS.md content
	if agentsMD != "" {
		sb.WriteString("# Project Context\n\n")
		sb.WriteString("Project-specific instructions and guidelines:\n\n")
		sb.WriteString(agentsMD)
		sb.WriteString("\n\n")
	}

	// Skills index (if provided)
	if len(skillsIndex) > 0 && skillsIndex[0] != "" {
		sb.WriteString(skillsIndex[0])
		sb.WriteString("\n\n")
	}

	// Current date/time and working directory
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	fmt.Fprintf(&sb, "Current date and time: %s\n", time.Now().Format("Monday, January 2, 2006 at 3:04:05 PM MST"))
	fmt.Fprintf(&sb, "Current working directory: %s\n", cwd)

	// Git context (injected by bootstrap if available)
	if gitCtx := git.Context(cwd); gitCtx != "" {
		sb.WriteString("\n## Git\n\n")
		sb.WriteString(gitCtx)
		sb.WriteString("\n")
	}

	return sb.String()
}
