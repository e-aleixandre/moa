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

	tool := NewTool(cwd)

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

	tool := NewTool(cwd)

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
	tool := NewTool(t.TempDir())
	result, err := tool.Execute(context.Background(), map[string]any{"name": ""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error result for empty name")
	}
}

func TestTool_MissingName(t *testing.T) {
	tool := NewTool(t.TempDir())
	result, err := tool.Execute(context.Background(), map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected error result for missing name")
	}
}

// The tool is built once per session but the workspace keeps changing: a skill
// written after startup has to be loadable without a restart.
func TestTool_SeesSkillsCreatedAfterItWasBuilt(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	tool := NewTool(cwd)

	res, err := tool.Execute(context.Background(), map[string]any{"name": "fresh"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("a skill that does not exist yet should not load")
	}

	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "fresh", "# Fresh\n\nWritten mid-session.\n")

	res, err = tool.Execute(context.Background(), map[string]any{"name": "fresh"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(res.Content[0].Text, "Written mid-session") {
		t.Errorf("a skill created after startup was not visible: %+v", res.Content[0].Text)
	}
}
