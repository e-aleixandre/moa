package bootstrap_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/e-aleixandre/moa/pkg/tool"
	"github.com/e-aleixandre/moa/pkg/verify"
)

// Pins the boundary the verify cwd parameter relies on, wired the way bootstrap
// wires it: a real PathPolicy rather than a test double. A workspace-scoped
// session must not run another directory's checks — a .moa/verify.json is shell
// commands — while an unrestricted (YOLO) session reaches anywhere, which is
// what asking for YOLO means.
func TestVerifyToolHonoursPathScope(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	writeVerifyConfig(t, outside)

	run := func(unrestricted bool) (string, bool) {
		policy := tool.NewPathPolicy(workspace, nil, unrestricted)
		tl := verify.NewTool(workspace, policy)
		r, err := tl.Execute(t.Context(), map[string]any{"cwd": outside}, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var text string
		for _, c := range r.Content {
			text += c.Text
		}
		return text, r.IsError
	}

	if out, isErr := run(false); !isErr {
		t.Fatalf("workspace scope should refuse an outside directory, got: %s", out)
	}
	if out, isErr := run(true); isErr {
		t.Fatalf("unrestricted scope should allow any directory, got: %s", out)
	}
}

func writeVerifyConfig(t *testing.T, dir string) {
	t.Helper()
	moaDir := filepath.Join(dir, ".moa")
	if err := os.MkdirAll(moaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(verify.Config{
		Checks: []verify.Check{{Name: "build", Command: "true"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moaDir, "verify.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
