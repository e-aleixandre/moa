package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, base, name, content string) {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, skillFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscover_ProjectAndGlobal(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)

	globalDir := filepath.Join(home, ".config", "moa", "skills")
	projectDir := filepath.Join(cwd, ".moa", "skills")

	writeSkill(t, globalDir, "shared", "# Shared Global\n\nGlobal version.\n")
	writeSkill(t, globalDir, "global-only", "# Global Only\n\nOnly in global.\n")
	writeSkill(t, projectDir, "shared", "# Shared Project\n\nProject version.\n")
	writeSkill(t, projectDir, "project-only", "# Project Only\n\nOnly in project.\n")

	skills := Discover(cwd)
	if len(skills) != 3 {
		t.Fatalf("expected 3 skills, got %d", len(skills))
	}

	byName := make(map[string]Skill)
	for _, s := range skills {
		byName[s.Name] = s
	}

	// "shared" should come from project (overrides global).
	if byName["shared"].DisplayName != "Shared Project" {
		t.Errorf("shared: expected project override, got %q", byName["shared"].DisplayName)
	}
	if _, ok := byName["global-only"]; !ok {
		t.Error("missing global-only skill")
	}
	if _, ok := byName["project-only"]; !ok {
		t.Error("missing project-only skill")
	}
}

func TestDiscover_Empty(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	skills := Discover(cwd)
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

func TestDiscover_ParsesHeading(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "go-testing",
		"# Go Testing Best Practices\n\nComprehensive guide for Go tests.\n\n- Use table-driven tests\n")

	skills := Discover(cwd)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := skills[0]
	if s.Name != "go-testing" {
		t.Errorf("name: got %q, want %q", s.Name, "go-testing")
	}
	if s.DisplayName != "Go Testing Best Practices" {
		t.Errorf("displayName: got %q, want %q", s.DisplayName, "Go Testing Best Practices")
	}
	if s.Description != "Comprehensive guide for Go tests." {
		t.Errorf("description: got %q, want %q", s.Description, "Comprehensive guide for Go tests.")
	}
}

func TestDiscover_NoHeading(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "plain",
		"Just some instructions without a heading.\n")

	skills := Discover(cwd)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	// Falls back to directory name.
	if skills[0].DisplayName != "plain" {
		t.Errorf("displayName: got %q, want %q", skills[0].DisplayName, "plain")
	}
}

func TestDiscover_SkipsNonDirs(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	skillsDir := filepath.Join(cwd, ".moa", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a file (not a directory) in skills/
	if err := os.WriteFile(filepath.Join(skillsDir, "not-a-skill.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills := Discover(cwd)
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

func TestDiscover_SkipsDirWithoutSkillMD(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	// Create a directory with no SKILL.md
	dir := filepath.Join(cwd, ".moa", "skills", "empty-skill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	skills := Discover(cwd)
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

func TestDiscover_Sorted(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	projectDir := filepath.Join(cwd, ".moa", "skills")
	writeSkill(t, projectDir, "zulu", "# Zulu\n")
	writeSkill(t, projectDir, "alpha", "# Alpha\n")
	writeSkill(t, projectDir, "mike", "# Mike\n")

	skills := Discover(cwd)
	if len(skills) != 3 {
		t.Fatalf("expected 3, got %d", len(skills))
	}
	if skills[0].Name != "alpha" || skills[1].Name != "mike" || skills[2].Name != "zulu" {
		t.Errorf("not sorted: %v", []string{skills[0].Name, skills[1].Name, skills[2].Name})
	}
}

func TestLoad(t *testing.T) {
	cwd := t.TempDir()
	content := "# Docker\n\nUse multi-stage builds.\n"
	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "docker", content)

	skills := Discover(cwd)
	if len(skills) != 1 {
		t.Fatalf("expected 1, got %d", len(skills))
	}

	loaded, err := Load(skills[0])
	if err != nil {
		t.Fatal(err)
	}
	if loaded != content {
		t.Errorf("loaded content mismatch:\ngot:  %q\nwant: %q", loaded, content)
	}
}

func TestParseSkillHeader_DescriptionStopsAtList(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "test-skill",
		"# My Skill\n\n- item one\n- item two\n")

	skills := Discover(cwd)
	if len(skills) != 1 {
		t.Fatalf("expected 1, got %d", len(skills))
	}
	// Description should be empty since first content after heading is a list.
	if skills[0].Description != "" {
		t.Errorf("expected empty description, got %q", skills[0].Description)
	}
}

func TestFormatIndex(t *testing.T) {
	skills := []Skill{
		{Name: "go-testing", DisplayName: "Go Testing", Description: "Best practices"},
		{Name: "docker", DisplayName: "Docker"},
	}
	idx := FormatIndex(skills)
	if !strings.Contains(idx, "go-testing: Go Testing — Best practices") {
		t.Errorf("missing go-testing entry: %s", idx)
	}
	if !strings.Contains(idx, "docker: Docker") {
		t.Errorf("missing docker entry: %s", idx)
	}
	// Docker has no description, should not have " — "
	if strings.Contains(idx, "Docker —") {
		t.Error("docker should not have description separator")
	}
}

func TestFormatIndex_Empty(t *testing.T) {
	if FormatIndex(nil) != "" {
		t.Error("expected empty string for nil skills")
	}
}

func TestParseSkillHeader_MultiLineDescription(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	writeSkill(t, filepath.Join(cwd, ".moa", "skills"), "multi",
		"# Multi\n\nFirst line of description\nsecond line of description.\n\nAnother paragraph.\n")

	skills := Discover(cwd)
	if len(skills) != 1 {
		t.Fatalf("expected 1, got %d", len(skills))
	}
	want := "First line of description second line of description."
	if skills[0].Description != want {
		t.Errorf("description: got %q, want %q", skills[0].Description, want)
	}
}
