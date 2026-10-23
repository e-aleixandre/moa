package agentcontext

import (
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

// The tool schemas already carry every name and description, so the system
// prompt must not repeat them as an inventory: that duplicate cost ~5.8K
// tokens per request on owners and moved the prompt whenever MCP tools changed.
func TestBuildSystemPrompt_NoToolInventory(t *testing.T) {
	tools := []core.ToolSpec{
		{Name: "read", Description: "Read a file."},
		{Name: "bash", Description: "Run a command."},
		{Name: "mcp__playwright__browser_click", Description: "Perform click on a web page"},
	}
	prompt := BuildSystemPrompt(SystemPromptOptions{Tools: tools, CWD: "/test"})
	if strings.Contains(prompt, "Available tools") {
		t.Fatalf("prompt still carries the tool inventory")
	}
	for _, line := range []string{"- read:", "- bash:", "- mcp__playwright__browser_click:"} {
		if strings.Contains(prompt, line) {
			t.Fatalf("prompt still lists %q", line)
		}
	}
	// Adaptive guidelines still depend on which tools exist.
	if !strings.Contains(prompt, "Use bash for file operations") {
		t.Fatalf("tool-dependent guidelines lost")
	}
}

// Rebuilding with a different MCP tool set must not change the prompt: only
// built-in names drive the guidelines.
func TestBuildSystemPrompt_StableAcrossMCPToolChanges(t *testing.T) {
	base := []core.ToolSpec{{Name: "read"}, {Name: "edit"}, {Name: "bash"}}
	with := append(append([]core.ToolSpec{}, base...), core.ToolSpec{Name: "mcp__lovable__list_projects", Description: "List projects"})
	a := BuildSystemPrompt(SystemPromptOptions{Tools: base, CWD: "/test", Git: new(string)})
	b := BuildSystemPrompt(SystemPromptOptions{Tools: with, CWD: "/test", Git: new(string)})
	if a != b {
		t.Fatalf("MCP tool change moved the system prompt")
	}
}
