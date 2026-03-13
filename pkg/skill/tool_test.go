package skill

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestTool_LoadSkill(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	content := "# Go Testing\n\nUse table-driven tests.\n"
	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "go-testing", content)

	skills := Discover(cwd)
	tool := NewTool(skills)

	result, err := tool.Execute(context.Background(), map[string]any{"name": "go-testing"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	if len(result.Content) != 1 || result.Content[0].Text != content {
		t.Errorf("got %q, want %q", result.Content[0].Text, content)
	}
}

func TestTool_NotFound(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "docker", "# Docker\n")
	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "security", "# Security\n")

	skills := Discover(cwd)
	tool := NewTool(skills)

	result, err := tool.Execute(context.Background(), map[string]any{"name": "nonexistent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error result")
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "nonexistent") {
		t.Errorf("error should mention the requested name, got: %s", text)
	}
	if !strings.Contains(text, "docker") || !strings.Contains(text, "security") {
		t.Errorf("error should list available skills, got: %s", text)
	}
}

func TestTool_EmptyName(t *testing.T) {
	tool := NewTool(nil)
	result, err := tool.Execute(context.Background(), map[string]any{"name": ""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error result for empty name")
	}
}

func TestTool_MissingName(t *testing.T) {
	tool := NewTool(nil)
	result, err := tool.Execute(context.Background(), map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error result for missing name")
	}
}
