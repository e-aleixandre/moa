package main

import (
	"context"
	"debug/buildinfo"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestBuild_NoCgo(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	output := filepath.Join(t.TempDir(), "moa")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", output, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if log, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("release-compatible binary did not build with CGO_ENABLED=0: %v\n%s", err, log)
	}
	info, err := buildinfo.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range info.Settings {
		if setting.Key == "CGO_ENABLED" && setting.Value == "0" {
			return
		}
	}
	t.Fatalf("built binary does not attest CGO_ENABLED=0: %v", info.Settings)
}
