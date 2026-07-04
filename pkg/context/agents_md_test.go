package agentcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAgentsMD_GlobalFromMoaConfigDir(t *testing.T) {
	globalDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(globalDir, "AGENTS.md"), []byte("GLOBAL RULES"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOA_CONFIG_DIR", globalDir)
	cwd := t.TempDir() // no AGENTS.md here
	out, err := LoadAgentsMD(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "GLOBAL RULES") {
		t.Fatalf("expected global AGENTS.md content loaded from MOA_CONFIG_DIR, got %q", out)
	}
}
