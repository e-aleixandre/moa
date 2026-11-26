package goal

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func readVia(t *testing.T, workDir, state, path string) bool {
	reg, err := newVerifierRegistry(workDir, state)
	if err != nil {
		t.Fatal(err)
	}
	rt, _ := reg.Get("read")
	res, err := rt.Execute(context.Background(), map[string]any{"path": path}, nil)
	return err == nil && !res.IsError
}

func TestReview_DotDotEscapeDenied(t *testing.T) {
	workDir, stateDir := t.TempDir(), t.TempDir()
	state := filepath.Join(stateDir, "STATE.md")
	mustPR167(t, os.WriteFile(state, []byte("x"), 0o644))
	mustPR167(t, os.WriteFile(filepath.Join(stateDir, "other.txt"), []byte("secret"), 0o644))
	if readVia(t, workDir, state, filepath.Join(stateDir, "STATE.md", "..", "other.txt")) {
		t.Error("STATE.md/../other.txt readable")
	}
}

// Maker replaces STATE.md with a symlink to a directory after the goal starts.
func TestReview_StateSymlinkToDirOpensSubtree(t *testing.T) {
	workDir, stateDir, secretDir := t.TempDir(), t.TempDir(), t.TempDir()
	mustPR167(t, os.WriteFile(filepath.Join(secretDir, "id_rsa"), []byte("secret"), 0o600))
	state := filepath.Join(stateDir, "STATE.md")
	if err := os.Symlink(secretDir, state); err != nil {
		t.Fatal(err)
	}
	if readVia(t, workDir, state, filepath.Join(secretDir, "id_rsa")) {
		t.Error("symlinked STATE.md opens reads of another directory's files")
	}
}

// Maker replaces STATE.md with a symlink to an arbitrary file.
func TestReview_StateSymlinkToFile(t *testing.T) {
	workDir, stateDir, secretDir := t.TempDir(), t.TempDir(), t.TempDir()
	secret := filepath.Join(secretDir, "id_rsa")
	mustPR167(t, os.WriteFile(secret, []byte("secret"), 0o600))
	state := filepath.Join(stateDir, "STATE.md")
	mustPR167(t, os.Symlink(secret, state))
	if readVia(t, workDir, state, secret) {
		t.Error("symlinked STATE.md target readable by its real path")
	}
}

func mustPR167(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
