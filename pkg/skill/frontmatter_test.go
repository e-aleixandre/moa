package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newSkillDir returns a workspace whose .moa/skills is ready for writeSkill.
func newSkillDir(t *testing.T) (root, skillsDir string) {
	t.Helper()
	root = t.TempDir()
	skillsDir = filepath.Join(root, ".moa", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, skillsDir
}

// A skill the model may not invoke costs prompt tokens on every request of the
// session while being unreachable to the model, so it must not be indexed.
func TestFormatIndex_OmitsSkillsTheModelCannotInvoke(t *testing.T) {
	root, dir := newSkillDir(t)
	writeSkill(t, dir, "landing", "---\ndisable-model-invocation: true\n---\n# Landing\n\nBuild a landing page.\n")
	writeSkill(t, dir, "design", "# Design\n\nDesign principles.\n")

	index := FormatIndex(Discover(root))

	if strings.Contains(index, "landing") {
		t.Errorf("user-only skill leaked into the prompt index:\n%s", index)
	}
	if !strings.Contains(index, "design") {
		t.Errorf("model-invocable skill missing from the index:\n%s", index)
	}
}

// With every skill user-only there is nothing to advertise, so the index header
// must not be emitted on its own.
func TestFormatIndex_EmptyWhenEverySkillIsUserOnly(t *testing.T) {
	root, dir := newSkillDir(t)
	writeSkill(t, dir, "landing", "---\ndisable-model-invocation: true\n---\n# Landing\n\nBuild a landing.\n")

	if got := FormatIndex(Discover(root)); got != "" {
		t.Errorf("expected an empty index, got:\n%s", got)
	}
}

func TestDiscover_ParsesInvocationFrontmatter(t *testing.T) {
	root, dir := newSkillDir(t)
	writeSkill(t, dir, "useronly", "---\ndisable-model-invocation: true\n---\n# User Only\n\nBody.\n")
	writeSkill(t, dir, "modelonly", "---\nuser-invocable: false\n---\n# Model Only\n\nBody.\n")
	writeSkill(t, dir, "plain", "# Plain\n\nBody.\n")

	byName := map[string]Skill{}
	for _, s := range Discover(root) {
		byName[s.Name] = s
	}

	if !byName["useronly"].DisableModelInvocation {
		t.Error("disable-model-invocation: true was not parsed")
	}
	if byName["useronly"].UserInvocable != true {
		t.Error("a user-only skill must stay user-invocable")
	}
	if byName["modelonly"].UserInvocable {
		t.Error("user-invocable: false was not parsed")
	}
	// Defaults: a skill without frontmatter is invocable by both, which is how
	// every skill written before this feature behaved.
	if byName["plain"].DisableModelInvocation || !byName["plain"].UserInvocable {
		t.Errorf("plain skill changed behaviour: %+v", byName["plain"])
	}
}

// The convention accepts yes/no/on/off/1/0 in any case; taking the default for
// those would silently ignore what the author wrote.
func TestFrontmatter_BoolSpellings(t *testing.T) {
	for _, spelling := range []string{"true", "True", "yes", "YES", "on", "1"} {
		root, dir := newSkillDir(t)
		writeSkill(t, dir, "s", "---\ndisable-model-invocation: "+spelling+"\n---\n# S\n\nBody.\n")
		if !Discover(root)[0].DisableModelInvocation {
			t.Errorf("%q was not read as true", spelling)
		}
	}
	for _, spelling := range []string{"false", "no", "off", "0"} {
		root, dir := newSkillDir(t)
		writeSkill(t, dir, "s", "---\ndisable-model-invocation: "+spelling+"\n---\n# S\n\nBody.\n")
		if Discover(root)[0].DisableModelInvocation {
			t.Errorf("%q was not read as false", spelling)
		}
	}
}

// The frontmatter configures moa; sending it to the model would spend tokens on
// keys it cannot act on.
func TestLoad_StripsFrontmatter(t *testing.T) {
	root, dir := newSkillDir(t)
	writeSkill(t, dir, "s", "---\ndisable-model-invocation: true\n---\n# Title\n\nThe body.\n")

	body, err := Load(Discover(root)[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "disable-model-invocation") {
		t.Errorf("frontmatter reached the model:\n%s", body)
	}
	if !strings.HasPrefix(body, "# Title") {
		t.Errorf("body should start at the heading, got:\n%s", body)
	}
}

// A document whose first line is not "---" is content, markers included: a
// horizontal rule must not be mistaken for configuration.
func TestLoad_KeepsContentWhenThereIsNoFrontmatter(t *testing.T) {
	root, dir := newSkillDir(t)
	content := "# Title\n\n---\n\nA rule above this line.\n"
	writeSkill(t, dir, "s", content)

	body, err := Load(Discover(root)[0])
	if err != nil {
		t.Fatal(err)
	}
	if body != content {
		t.Errorf("content was altered:\ngot:\n%s\nwant:\n%s", body, content)
	}
}

// The heading lives below the frontmatter; parsing must not stop at the marker.
func TestDiscover_ReadsHeadingBelowFrontmatter(t *testing.T) {
	root, dir := newSkillDir(t)
	writeSkill(t, dir, "s", "---\nuser-invocable: false\n---\n# Real Title\n\nThe description.\n")

	s := Discover(root)[0]
	if s.DisplayName != "Real Title" {
		t.Errorf("DisplayName = %q, want %q", s.DisplayName, "Real Title")
	}
	if s.Description != "The description." {
		t.Errorf("Description = %q, want %q", s.Description, "The description.")
	}
}
