package prompt

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemplate(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscover_Templates(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())

	projectDir := filepath.Join(cwd, ".moa", "prompts")
	writeTemplate(t, projectDir, "refactor", "Refactoriza {{file}} para mejorar la legibilidad.\n")
	writeTemplate(t, projectDir, "review", "Revisa {{file}} buscando bugs.\n")

	templates := Discover(cwd)
	if len(templates) != 2 {
		t.Fatalf("expected 2, got %d", len(templates))
	}
	if templates[0].Name != "refactor" || templates[1].Name != "review" {
		t.Errorf("unexpected names: %v, %v", templates[0].Name, templates[1].Name)
	}
	if len(templates[0].Placeholders) != 1 || templates[0].Placeholders[0] != "file" {
		t.Errorf("expected placeholder 'file', got %v", templates[0].Placeholders)
	}
}

func TestDiscover_ProjectOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("HOME", home)

	globalDir := filepath.Join(home, ".config", "moa", "prompts")
	projectDir := filepath.Join(cwd, ".moa", "prompts")

	writeTemplate(t, globalDir, "refactor", "Global version.\n")
	writeTemplate(t, globalDir, "global-only", "Only global.\n")
	writeTemplate(t, projectDir, "refactor", "Project version.\n")

	templates := Discover(cwd)
	if len(templates) != 2 {
		t.Fatalf("expected 2, got %d", len(templates))
	}

	byName := make(map[string]Template)
	for _, tmpl := range templates {
		byName[tmpl.Name] = tmpl
	}

	if byName["refactor"].Content != "Project version.\n" {
		t.Errorf("refactor should be project version, got %q", byName["refactor"].Content)
	}
	if _, ok := byName["global-only"]; !ok {
		t.Error("missing global-only template")
	}
}

func TestDiscover_Empty(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	templates := Discover(cwd)
	if len(templates) != 0 {
		t.Errorf("expected 0, got %d", len(templates))
	}
}

func TestDiscover_SkipsNonMD(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(cwd, ".moa", "prompts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a template"), 0o644); err != nil {
		t.Fatal(err)
	}
	templates := Discover(cwd)
	if len(templates) != 0 {
		t.Errorf("expected 0, got %d", len(templates))
	}
}

func TestDiscover_SkipsDirectories(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(cwd, ".moa", "prompts", "subdir.md")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	templates := Discover(cwd)
	if len(templates) != 0 {
		t.Errorf("expected 0, got %d", len(templates))
	}
}

func TestRender(t *testing.T) {
	tmpl := Template{
		Name:         "test",
		Content:      "Review {{file}} for {{issue}} problems.",
		Placeholders: []string{"file", "issue"},
	}
	result, err := Render(tmpl, map[string]string{
		"file":  "main.go",
		"issue": "performance",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "Review main.go for performance problems."
	if result != want {
		t.Errorf("got %q, want %q", result, want)
	}
}

func TestRender_MissingValue(t *testing.T) {
	tmpl := Template{
		Name:         "test",
		Content:      "Review {{file}} for {{issue}} problems.",
		Placeholders: []string{"file", "issue"},
	}
	_, err := Render(tmpl, map[string]string{"file": "main.go"})
	if err == nil {
		t.Fatal("expected error for missing placeholder")
	}
}

func TestRender_NoPlaceholders(t *testing.T) {
	tmpl := Template{
		Name:    "simple",
		Content: "Run all tests and fix failures.",
	}
	result, err := Render(tmpl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != tmpl.Content {
		t.Errorf("got %q, want %q", result, tmpl.Content)
	}
}

func TestRender_DuplicatePlaceholder(t *testing.T) {
	tmpl := Template{
		Name:         "dup",
		Content:      "Compare {{file}} old vs {{file}} new.",
		Placeholders: []string{"file"},
	}
	result, err := Render(tmpl, map[string]string{"file": "main.go"})
	if err != nil {
		t.Fatal(err)
	}
	want := "Compare main.go old vs main.go new."
	if result != want {
		t.Errorf("got %q, want %q", result, want)
	}
}

func TestExtractPlaceholders_Unique(t *testing.T) {
	placeholders := extractPlaceholders("{{a}} and {{b}} and {{a}} again")
	if len(placeholders) != 2 {
		t.Fatalf("expected 2 unique, got %d", len(placeholders))
	}
	if placeholders[0] != "a" || placeholders[1] != "b" {
		t.Errorf("unexpected: %v", placeholders)
	}
}
