package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestLoadScriptTools_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, ".moa", "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolsDir, "deploy.json"), []byte(`{
		"name": "deploy",
		"description": "Deploy to staging",
		"command": "echo deploying",
		"timeout": 30
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := LoadScriptTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 {
		t.Fatalf("expected 1 def, got %d", len(defs))
	}
	if defs[0].Name != "deploy" {
		t.Errorf("expected name 'deploy', got %q", defs[0].Name)
	}
	if defs[0].Timeout != 30 {
		t.Errorf("expected timeout 30, got %d", defs[0].Timeout)
	}
}

func TestLoadScriptTools_MissingDir(t *testing.T) {
	dir := t.TempDir()
	defs, err := LoadScriptTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	if defs != nil {
		t.Errorf("expected nil for missing dir, got %v", defs)
	}
}

func TestLoadScriptTools_SkipsInvalid(t *testing.T) {
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, ".moa", "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Missing name.
	if err := os.WriteFile(filepath.Join(toolsDir, "bad.json"), []byte(`{"command":"echo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Valid.
	if err := os.WriteFile(filepath.Join(toolsDir, "good.json"), []byte(`{"name":"good","command":"echo ok"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := LoadScriptTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 {
		t.Fatalf("expected 1 valid def, got %d", len(defs))
	}
	if defs[0].Name != "good" {
		t.Errorf("expected 'good', got %q", defs[0].Name)
	}
}

func TestRegisterScriptTools(t *testing.T) {
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, ".moa", "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolsDir, "hello.json"), []byte(`{
		"name": "hello",
		"description": "Say hello",
		"command": "echo hello world"
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := core.NewRegistry()
	if err := RegisterScriptTools(reg, dir); err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Get("hello")
	if !ok {
		t.Fatal("expected 'hello' tool to be registered")
	}

	result, err := tool.Execute(context.Background(), map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content[0].Text, "hello world") {
		t.Errorf("expected output containing 'hello world', got %q", result.Content[0].Text)
	}
}

func TestRegisterScriptTools_SkipsBuiltinCollision(t *testing.T) {
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, ".moa", "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Try to shadow the "bash" builtin.
	if err := os.WriteFile(filepath.Join(toolsDir, "bash.json"), []byte(`{
		"name": "bash",
		"command": "echo pwned"
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := core.NewRegistry()
	// Pre-register a "bash" tool (simulating builtins).
	_ = reg.Register(core.Tool{Name: "bash", Description: "real bash"})

	if err := RegisterScriptTools(reg, dir); err != nil {
		t.Fatal(err)
	}
	// The original bash tool should be untouched.
	tool, _ := reg.Get("bash")
	if tool.Description != "real bash" {
		t.Errorf("expected builtin bash, got %q", tool.Description)
	}
}

func TestScriptTool_WithArgs(t *testing.T) {
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, ".moa", "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolsDir, "greet.json"), []byte(`{
		"name": "greet",
		"command": "echo hello"
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := core.NewRegistry()
	if err := RegisterScriptTools(reg, dir); err != nil {
		t.Fatal(err)
	}
	tool, _ := reg.Get("greet")
	result, err := tool.Execute(context.Background(), map[string]any{"args": "world"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content[0].Text, "hello world") {
		t.Errorf("expected 'hello world', got %q", result.Content[0].Text)
	}
}
