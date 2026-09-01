package agentcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSourcesAgentsMD(t *testing.T, cwd, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cwd, "AGENTS.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Reload records the new disk state before the caller applies it. If applying
// fails (SetSystemPrompt refuses mid-run) and the caller does not revert, the
// next Reload sees no difference and the edit is lost for good.
func TestSources_ReloadRevertKeepsTheChangeVisible(t *testing.T) {
	t.Setenv("MOA_CONFIG_DIR", t.TempDir())
	cwd := t.TempDir()
	writeSourcesAgentsMD(t, cwd, "- One.\n")

	initial, err := LoadAgentsMD(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	src := NewSources(cwd, nil, initial, "", "")

	writeSourcesAgentsMD(t, cwd, "- One.\n- Two.\n")
	changed, revert := src.Reload()
	if len(changed) == 0 {
		t.Fatal("Reload did not see the edited AGENTS.md")
	}

	revert()
	agentsMD, _, _ := src.Snapshot()
	if strings.Contains(agentsMD, "Two") {
		t.Fatal("revert left the new AGENTS.md recorded")
	}

	changed, _ = src.Reload()
	if len(changed) == 0 {
		t.Fatal("the change was lost: a retry after revert saw nothing to do")
	}
	agentsMD, _, _ = src.Snapshot()
	if !strings.Contains(agentsMD, "Two") {
		t.Fatalf("retry did not store the new AGENTS.md: %q", agentsMD)
	}
}
