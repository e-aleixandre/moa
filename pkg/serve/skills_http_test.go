package serve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/skill"
)

func writeTestSkill(t *testing.T, cwd, name, content string) {
	t.Helper()
	dir := filepath.Join(cwd, ".moa", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A skill must never take over a built-in: shadowing /compact would remove a
// command the user relies on, and the slash menu would still look correct.
func TestSkillCommands_BuiltinKeepsTheBareName(t *testing.T) {
	cwd := t.TempDir()
	writeTestSkill(t, cwd, "compact", "# Compact\n\nA skill named like a command.\n")
	writeTestSkill(t, cwd, "deploy", "# Deploy\n\nShip it.\n")

	byName := map[string]SkillCommand{}
	for _, c := range skillCommands(skill.Discover(cwd)) {
		byName[c.Name] = c
	}

	if _, taken := byName["compact"]; taken {
		t.Error("a skill claimed the bare name of the /compact command")
	}
	c, ok := byName["skill:compact"]
	if !ok {
		t.Fatalf("colliding skill was dropped instead of prefixed: %v", byName)
	}
	if c.Skill != "compact" {
		t.Errorf("Skill = %q, want the unprefixed name", c.Skill)
	}
	if _, ok := byName["deploy"]; !ok {
		t.Error("a non-colliding skill should keep its bare name")
	}
}

// /secret is implemented by the frontend and never reaches the server, so it is
// absent from the command registry — but a skill must not claim it either.
func TestSkillCommands_ReservesFrontendOnlyCommands(t *testing.T) {
	cwd := t.TempDir()
	writeTestSkill(t, cwd, "secret", "# Secret\n\nBody.\n")

	for _, c := range skillCommands(skill.Discover(cwd)) {
		if c.Name == "secret" {
			t.Error("a skill claimed /secret, which the composer handles itself")
		}
	}
}

// A skill marked as model-only is not an action the user invokes.
func TestSkillCommands_OmitsSkillsTheUserCannotInvoke(t *testing.T) {
	cwd := t.TempDir()
	writeTestSkill(t, cwd, "background", "---\nuser-invocable: false\n---\n# Background\n\nContext.\n")

	if got := skillCommands(skill.Discover(cwd)); len(got) != 0 {
		t.Errorf("model-only skill offered in the slash menu: %+v", got)
	}
}

func TestFindInvocableSkill(t *testing.T) {
	cwd := t.TempDir()
	writeTestSkill(t, cwd, "deploy", "# Deploy\n\nShip.\n")
	writeTestSkill(t, cwd, "compact", "# Compact\n\nCollides.\n")
	writeTestSkill(t, cwd, "background", "---\nuser-invocable: false\n---\n# B\n\nCtx.\n")

	if _, ok := findInvocableSkill(cwd, "deploy"); !ok {
		t.Error("bare name did not resolve")
	}
	// The prefixed form is the only way to reach a colliding skill.
	if _, ok := findInvocableSkill(cwd, "skill:compact"); !ok {
		t.Error("skill: prefix did not resolve a colliding skill")
	}
	if _, ok := findInvocableSkill(cwd, "background"); ok {
		t.Error("a model-only skill must not be invocable by name")
	}
	if _, ok := findInvocableSkill(cwd, "nope"); ok {
		t.Error("unknown name resolved")
	}
}

func TestRenderSkillBody(t *testing.T) {
	t.Run("substitutes the placeholder", func(t *testing.T) {
		got := renderSkillBody("Fix issue $ARGUMENTS now.", []string{"123"})
		if got != "Fix issue 123 now." {
			t.Errorf("got %q", got)
		}
	})

	// Dropping the arguments would lose the only part of the invocation the
	// user typed by hand.
	t.Run("appends arguments when there is no placeholder", func(t *testing.T) {
		got := renderSkillBody("# Deploy\n\nShip it.\n", []string{"staging", "--fast"})
		if !strings.Contains(got, "ARGUMENTS: staging --fast") {
			t.Errorf("arguments were dropped:\n%s", got)
		}
		if !strings.HasPrefix(got, "# Deploy") {
			t.Errorf("body was altered:\n%s", got)
		}
	})

	t.Run("leaves the body alone without arguments", func(t *testing.T) {
		body := "# Deploy\n\nShip it.\n"
		if got := renderSkillBody(body, nil); got != body {
			t.Errorf("got %q, want %q", got, body)
		}
	})

	// An empty invocation must clear the placeholder, not leave it as literal
	// text for the model to read as an instruction.
	t.Run("clears the placeholder without arguments", func(t *testing.T) {
		if got := renderSkillBody("Do $ARGUMENTS.", nil); got != "Do ." {
			t.Errorf("got %q", got)
		}
	})
}

// Command lookup ignores case, so a skill named "Compact" collides with
// /compact just as "compact" does.
func TestSkillCommands_CollisionIgnoresCase(t *testing.T) {
	cwd := t.TempDir()
	writeTestSkill(t, cwd, "Compact", "# C\n\nBody.\n")

	got := skillCommands(skill.Discover(cwd))
	if len(got) != 1 || got[0].Name != "skill:Compact" {
		t.Fatalf("a differently-cased skill escaped the collision rule: %+v", got)
	}
	if _, ok := findInvocableSkill(cwd, "Compact"); ok {
		t.Error("/Compact resolved to the skill instead of the built-in command")
	}
	if _, ok := findInvocableSkill(cwd, "skill:compact"); !ok {
		t.Error("the prefixed form should resolve regardless of case")
	}
}

// A name that cannot survive the command line must not be advertised: the menu
// would show an entry that does nothing.
func TestSkillCommands_SkipsUnusableNames(t *testing.T) {
	cwd := t.TempDir()
	// Already prefixed: indistinguishable from a prefixed collision.
	writeTestSkill(t, cwd, "skill:deploy", "# D\n\nBody.\n")
	// Whitespace: the command parser splits on it.
	writeTestSkill(t, cwd, "two words", "# T\n\nBody.\n")

	if got := skillCommands(skill.Discover(cwd)); len(got) != 0 {
		t.Errorf("unusable names were offered: %+v", got)
	}
	if _, ok := findInvocableSkill(cwd, "skill:deploy"); ok {
		t.Error("a skill literally named skill:deploy must not resolve")
	}
}
