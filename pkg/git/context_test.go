package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestContext_InGitRepo(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "git", "init")
	mustRun(t, dir, "git", "config", "user.email", "test@test.com")
	mustRun(t, dir, "git", "config", "user.name", "Test")

	// Create a file and commit.
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "add", ".")
	mustRun(t, dir, "git", "commit", "-m", "initial commit")

	result := Context(dir)
	if result == "" {
		t.Fatal("expected non-empty context in git repo")
	}
	if !strings.Contains(result, "Branch:") {
		t.Error("expected Branch in context")
	}
	if !strings.Contains(result, "Last commit:") {
		t.Error("expected Last commit in context")
	}
	// No uncommitted changes after commit.
	if strings.Contains(result, "Uncommitted") {
		t.Error("expected no uncommitted changes after clean commit")
	}
}

func TestContext_WithDirtyFiles(t *testing.T) {
	dir := t.TempDir()
	mustRun(t, dir, "git", "init")
	mustRun(t, dir, "git", "config", "user.email", "test@test.com")
	mustRun(t, dir, "git", "config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "add", ".")
	mustRun(t, dir, "git", "commit", "-m", "initial")

	// Create dirty file.
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := Context(dir)
	if !strings.Contains(result, "Uncommitted changes") {
		t.Error("expected uncommitted changes")
	}
	if !strings.Contains(result, "dirty.txt") {
		t.Error("expected dirty.txt in changes")
	}
}

func TestContext_NotARepo(t *testing.T) {
	dir := t.TempDir()
	result := Context(dir)
	if result != "" {
		t.Errorf("expected empty for non-repo, got %q", result)
	}
}

func TestContext_EmptyString(t *testing.T) {
	// Edge case: empty cwd should not panic.
	result := Context("")
	// May or may not return data depending on CWD; just verify no panic.
	_ = result
}

func mustRun(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
	}
}
