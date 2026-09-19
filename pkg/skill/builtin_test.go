package skill

import (
	"path/filepath"
	"strings"
	"testing"
)

// The owner's role prompt tells it to load book-init. If the skill stops
// shipping in the binary, that instruction becomes a dead end on every machine
// where nobody copied the file by hand.
func TestBuiltin_BookInitShips(t *testing.T) {
	configDir := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", configDir)

	skills := Discover(cwd, Options{Builtin: true})
	if len(skills) != 1 {
		t.Fatalf("expected 1 built-in skill, got %d", len(skills))
	}
	s := skills[0]
	if s.Name != "book-init" {
		t.Fatalf("name: got %q", s.Name)
	}
	if !s.Builtin {
		t.Error("skill is not marked as built-in")
	}
	if s.Dir != "" {
		t.Errorf("built-in skill should have no directory, got %q", s.Dir)
	}
	if s.DisplayName == "" || s.Description == "" {
		t.Fatalf("heading and description feed the prompt index: %q / %q", s.DisplayName, s.Description)
	}
	if !s.ModelInvocable() || !s.UserInvocable {
		t.Error("book-init must be reachable by both the owner and the user")
	}

	body, err := Load(s)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, want := range []string{"INGEST.md", "Producto", "Uso", "Implementación"} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not mention %q", want)
		}
	}
}

// Built-ins are opt-in: an ordinary session's prompt index must not carry a
// skill that only means something to an owner.
func TestBuiltin_NotDiscoveredByDefault(t *testing.T) {
	configDir := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", configDir)

	if skills := Discover(cwd); len(skills) != 0 {
		t.Fatalf("expected no skills without Options.Builtin, got %d", len(skills))
	}
	if idx := FormatIndex(Discover(cwd)); idx != "" {
		t.Fatalf("expected empty index, got %q", idx)
	}
}

// A shipped skill the user cannot edit is a shipped skill they have to live
// with. Their own copy wins, and Load then reads it from disk.
func TestBuiltin_OverriddenByUserCopy(t *testing.T) {
	configDir := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", configDir)
	writeSkill(t, filepath.Join(configDir, "skills"), "book-init", "# Mine\n\nMy own version.\n")

	skills := Discover(cwd, Options{Builtin: true})
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := skills[0]
	if s.DisplayName != "Mine" {
		t.Fatalf("expected the user copy to win, got %q", s.DisplayName)
	}
	if s.Builtin {
		t.Error("the user copy must not be marked built-in")
	}
	body, err := Load(s)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !strings.Contains(body, "My own version.") {
		t.Errorf("loaded the embedded copy instead of the user's: %q", body)
	}
}

// load_skill and the prompt index must agree: a session offered the built-in
// can load it, and one that was not, cannot.
func TestTool_BuiltinFollowsSession(t *testing.T) {
	configDir := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", configDir)

	owner := NewTool(cwd, ToolConfig{Builtin: true})
	res, err := owner.Execute(t.Context(), map[string]any{"name": "book-init"}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.IsError {
		t.Fatal("owner session could not load book-init")
	}

	plain := NewTool(cwd)
	res, err = plain.Execute(t.Context(), map[string]any{"name": "book-init"}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !res.IsError {
		t.Fatal("an ordinary session must not be able to load book-init")
	}
}

// Discovery reads only the head of a skill file: it runs for every skill on
// every session build and every reload, and it never needs more than the
// frontmatter and the first paragraph.
func TestDiscover_ReadsOnlyTheHead(t *testing.T) {
	configDir := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("MOA_CONFIG_DIR", configDir)

	body := strings.Repeat("filler line that nobody needs at discovery time\n", 4000)
	writeSkill(t, filepath.Join(configDir, "skills"), "huge",
		"---\ncontext: fork\n---\n\n# Huge\n\nA skill with a very long body.\n\n"+body)

	skills := Discover(cwd)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := skills[0]
	if s.DisplayName != "Huge" || s.Description != "A skill with a very long body." {
		t.Fatalf("head not parsed: %q / %q", s.DisplayName, s.Description)
	}
	if !s.IsFork() {
		t.Error("frontmatter above the heading was not parsed")
	}
	// The body is only read when the skill is actually loaded, and there it is
	// capped with a marker rather than refused.
	loaded, err := Load(s)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) <= maxSkillBytes {
		t.Fatalf("expected the full body at load time, got %d bytes", len(loaded))
	}
	if !strings.Contains(loaded, "[skill truncated: over 50KB]") {
		t.Error("oversized body loaded without its truncation marker")
	}
}
