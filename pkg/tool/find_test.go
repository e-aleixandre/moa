package tool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildFindArgsUsesFdfind(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "fdfind")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	args := buildFindArgs("*.go", ".", "f")
	if args[0] != fake {
		t.Fatalf("expected %s, got %v", fake, args)
	}
}
