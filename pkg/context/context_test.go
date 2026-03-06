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

	os.WriteFile(filepath.Join(globalDir, "AGENTS.md"), []byte("# Global rules"), 0o644)

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
	os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("# Project rules"), 0o644)

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
	os.Mkdir(sub, 0o755)

	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT"), 0o644)
	os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("SUB"), 0o644)

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
	os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte("ONCE"), 0o644)

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

	prompt := BuildSystemPrompt("# Project: test", tools)

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
}

func TestBuildSystemPrompt_Empty(t *testing.T) {
	prompt := BuildSystemPrompt("", nil)
	if !strings.Contains(prompt, "coding agent") {
		t.Error("expected role even with no AGENTS.md")
	}
}


