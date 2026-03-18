package agentcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestLoadAgentsMD_Global(t *testing.T) {
	globalDir := t.TempDir()
	cwd := t.TempDir()

	_ = os.WriteFile(filepath.Join(globalDir, "AGENTS.md"), []byte("# Global rules"), 0o644)

	content, err := LoadAgentsMD(cwd, globalDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Global rules") {
		t.Fatalf("expected global content: %q", content)
	}
}

func TestLoadAgentsMD_ProjectLocal(t *testing.T) {
	cwd := t.TempDir()
	_ = os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# Project rules"), 0o644)

	content, err := LoadAgentsMD(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Project rules") {
		t.Fatalf("expected project content: %q", content)
	}
}

func TestLoadAgentsMD_Priority(t *testing.T) {
	// Create: /tmp/root/AGENTS.md and /tmp/root/sub/AGENTS.md
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	_ = os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT"), 0o644)
	_ = os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("SUB"), 0o644)

	content, err := LoadAgentsMD(sub, "")
	if err != nil {
		t.Fatal(err)
	}

	// ROOT should come before SUB (ancestor first, CWD last)
	rootIdx := strings.Index(content, "ROOT")
	subIdx := strings.Index(content, "SUB")
	if rootIdx < 0 || subIdx < 0 {
		t.Fatalf("expected both sections: %q", content)
	}
	if rootIdx >= subIdx {
		t.Fatal("expected ROOT before SUB (ancestors first)")
	}
}

func TestLoadAgentsMD_Dedup(t *testing.T) {
	cwd := t.TempDir()
	_ = os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("ONCE"), 0o644)

	// Even with cwd == agentHome, should only appear once
	content, err := LoadAgentsMD(cwd, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(content, "ONCE") != 1 {
		t.Fatalf("expected content exactly once: %q", content)
	}
}

func TestLoadAgentsMD_NoFiles(t *testing.T) {
	cwd := t.TempDir()
	content, err := LoadAgentsMD(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if content != "" {
		t.Fatalf("expected empty: %q", content)
	}
}

func TestBuildSystemPrompt(t *testing.T) {
	tools := []core.ToolSpec{
		{Name: "bash", Description: "Execute commands"},
		{Name: "read", Description: "Read files"},
	}

	prompt := BuildSystemPrompt(SystemPromptOptions{
		AgentsMD: "# Project: test",
		Tools:    tools,
		CWD:      "/test/cwd",
	})

	if !strings.Contains(prompt, "coding agent") {
		t.Error("expected role description")
	}
	if !strings.Contains(prompt, "bash:") {
		t.Error("expected tool description")
	}
	if !strings.Contains(prompt, "Project: test") {
		t.Error("expected AGENTS.md content")
	}
	if !strings.Contains(prompt, "Current date") {
		t.Error("expected date")
	}
	if !strings.Contains(prompt, "/test/cwd") {
		t.Error("expected CWD in prompt")
	}
}

func TestBuildSystemPrompt_WithVerify(t *testing.T) {
	tools := []core.ToolSpec{
		{Name: "bash", Description: "Execute commands"},
		{Name: "verify", Description: "Run verification checks"},
	}
	prompt := BuildSystemPrompt(SystemPromptOptions{Tools: tools, CWD: "/test", HasVerify: true})
	if !strings.Contains(prompt, "call the verify tool") {
		t.Error("expected verify guideline when hasVerify=true and verify tool present")
	}
}

func TestBuildSystemPrompt_VerifyFalseNoGuideline(t *testing.T) {
	tools := []core.ToolSpec{
		{Name: "bash", Description: "Execute commands"},
		{Name: "verify", Description: "Run verification checks"},
	}
	prompt := BuildSystemPrompt(SystemPromptOptions{Tools: tools, CWD: "/test"})
	if strings.Contains(prompt, "call the verify tool") {
		t.Error("expected no verify guideline when hasVerify=false")
	}
}

func TestBuildSystemPrompt_Empty(t *testing.T) {
	prompt := BuildSystemPrompt(SystemPromptOptions{})
	if !strings.Contains(prompt, "coding agent") {
		t.Error("expected role even with no AGENTS.md")
	}
}

func TestBuildSystemPrompt_WithSkills(t *testing.T) {
	tools := []core.ToolSpec{
		{Name: "load_skill", Description: "Load a skill pack"},
	}
	skillsIndex := "Available skills (use the load_skill tool to load when relevant):\n- go-testing: Go Testing — Best practices for Go tests\n"
	prompt := BuildSystemPrompt(SystemPromptOptions{Tools: tools, CWD: "/test", SkillsIndex: skillsIndex})
	if !strings.Contains(prompt, "go-testing: Go Testing") {
		t.Error("expected skills index in prompt")
	}
	if !strings.Contains(prompt, "load_skill") {
		t.Error("expected load_skill tool in prompt")
	}
}

func TestBuildSystemPrompt_EmptySkills(t *testing.T) {
	prompt := BuildSystemPrompt(SystemPromptOptions{CWD: "/test"})
	if strings.Contains(prompt, "Available skills") {
		t.Error("empty skills index should not appear")
	}
}

func TestBuildSystemPrompt_WithMemory(t *testing.T) {
	prompt := BuildSystemPrompt(SystemPromptOptions{
		AgentsMD:      "# Project",
		CWD:           "/test",
		MemoryContent: "- Always use Docker\n- Prefer PostgreSQL",
		SkillsIndex:   "Some skills here",
	})
	if !strings.Contains(prompt, "Project Memory") {
		t.Error("expected Project Memory section")
	}
	if !strings.Contains(prompt, "Always use Docker") {
		t.Error("expected memory content")
	}
	// Memory should be between AGENTS.md and skills
	agentsIdx := strings.Index(prompt, "# Project")
	memoryIdx := strings.Index(prompt, "Project Memory")
	skillsIdx := strings.Index(prompt, "Some skills here")
	if agentsIdx >= memoryIdx {
		t.Error("memory should come after AGENTS.md")
	}
	if memoryIdx >= skillsIdx {
		t.Error("memory should come before skills")
	}
}

func TestBuildSystemPrompt_EmptyMemory(t *testing.T) {
	prompt := BuildSystemPrompt(SystemPromptOptions{CWD: "/test"})
	if strings.Contains(prompt, "Project Memory") {
		t.Error("empty memory should not appear in prompt")
	}
}

func TestBuildSystemPrompt_MemoryGuideline(t *testing.T) {
	tools := []core.ToolSpec{
		{Name: "memory", Description: "Memory tool"},
	}
	prompt := BuildSystemPrompt(SystemPromptOptions{Tools: tools, CWD: "/test"})
	if !strings.Contains(prompt, "memory tool for future sessions") {
		t.Error("expected memory guideline when memory tool is available")
	}
}


