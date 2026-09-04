package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestHooksAddListRm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()

	if err := hooksMain([]string{"add", "sentry-tienda", "--project", dir, "--when-none", "create", "--model", "terra"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	cfg := core.LoadGlobalConfig()
	src, ok := cfg.Events.Source("sentry-tienda")
	if !ok {
		t.Fatal("source not stored")
	}
	if src.Secret == "" || src.Target.Kind != core.EventTargetProject {
		t.Fatalf("stored source = %+v", src)
	}
	abs, _ := filepath.Abs(dir)
	if src.Target.Project != abs {
		t.Fatalf("project = %q, want %q", src.Target.Project, abs)
	}
	if src.WhenNoneOrDefault() != core.EventWhenCreate || src.Create.Model != "terra" {
		t.Fatalf("create config = %+v", src)
	}
	if src.AutorunEnabled() {
		t.Fatal("add without --autorun should leave autorun off")
	}

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	err = hooksMain([]string{"list"})
	_ = w.Close()
	os.Stdout = old
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	out := string(buf[:n])
	if !strings.Contains(out, "sentry-tienda") || !strings.Contains(out, "********") || strings.Contains(out, src.Secret) {
		t.Fatalf("list leaked or missed the source: %q", out)
	}

	if err := hooksMain([]string{"rm", "sentry-tienda"}); err != nil {
		t.Fatalf("rm: %v", err)
	}
	cfg = core.LoadGlobalConfig()
	if _, ok := cfg.Events.Source("sentry-tienda"); ok {
		t.Fatal("source still present after rm")
	}
}

func TestHooksAddRequiresTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := hooksMain([]string{"add", "ci"}); err == nil {
		t.Fatal("add without a target succeeded")
	}
}

func TestHooksAddPrintsHookPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	err = hooksMain([]string{"add", "ci", "--inbox"})
	_ = w.Close()
	os.Stdout = old
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	out := string(buf[:n])
	if !strings.Contains(out, "Hook URL path (contains the secret; store it in the provider now):") {
		t.Fatalf("missing hook-path prompt: %q", out)
	}
	if !strings.Contains(out, "/hooks/ci/") {
		t.Fatalf("missing hook path: %q", out)
	}
}

func TestHooksAddAutorunFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := hooksMain([]string{"add", "ci", "--inbox", "--autorun"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	cfg := core.LoadGlobalConfig()
	src, ok := cfg.Events.Source("ci")
	if !ok || !src.AutorunEnabled() {
		t.Fatalf("autorun not stored: ok=%v src=%+v", ok, src)
	}
}
