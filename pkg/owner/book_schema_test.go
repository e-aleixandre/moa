package owner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/book"
	"github.com/e-aleixandre/moa/pkg/core"
)

// The template is a shape, not content: there is no example area, because a
// seeded sheet about nothing is a sheet the owner has to recognise as fake on
// every listing and every search — and it is also what "the book is still the
// template" is detected by.
func TestSeedBookWritesTheWholeSchema(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	own, err := store.Create(t.TempDir(), "Winerim", "", "", false, Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	bookDir := store.BookDir(own.CodebaseKey)
	for rel := range book.Template() {
		if _, err := os.Stat(filepath.Join(bookDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("template file %s missing: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(bookDir, "areas", "example")); !os.IsNotExist(err) {
		t.Fatalf("a fake area was seeded: %v", err)
	}
	sheets, err := filepath.Glob(filepath.Join(bookDir, "areas", "*", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sheets) != 0 {
		t.Fatalf("a fresh book already has sheets: %v", sheets)
	}
	// The shape a sheet has to follow is explained in the areas README, which
	// is where it can be read without being mistaken for a real sheet.
	areas, err := os.ReadFile(filepath.Join(bookDir, "areas", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Producto", "Uso", "Implementación", "aliases", "coverage"} {
		if !strings.Contains(string(areas), want) {
			t.Fatalf("areas/README.md does not explain %q:\n%s", want, areas)
		}
	}
	index := store.ProjectIndex(own.CodebaseKey)
	for _, want := range []string{"## Areas", "## Work in progress", "## Decisions that bind"} {
		if !strings.Contains(index, want) {
			t.Fatalf("PROJECT.md template missing %q:\n%s", want, index)
		}
	}
}

// Seeding never overwrites, and never silently skips either: a file that
// exists is kept, and an error that is not "it exists" fails the creation
// rather than being read as an absence.
func TestSeedBookNeverOverwritesAndFailsLoudly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	store := NewStore(t.TempDir())
	root := t.TempDir()
	key := storeKeyOf(t, store, root)
	bookDir := store.BookDir(key)
	if err := os.MkdirAll(bookDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A file that is already there is the user's and is kept, whatever the
	// template says.
	mine := filepath.Join(bookDir, "people.md")
	if err := os.WriteFile(mine, []byte("# Mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// And a book that cannot be written fails the creation: an error that is
	// not "it already exists" must never be read as "it is absent", which is
	// how a template overwrites a book.
	if err := os.Chmod(bookDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bookDir, 0o700) })

	if _, err := store.Create(root, "Winerim", "", "", false, Avatar{}); err == nil {
		t.Fatal("a seed that could not write a template file reported success")
	}
	if got, err := os.ReadFile(mine); err != nil || string(got) != "# Mine\n" {
		t.Fatalf("an existing book file was overwritten: %q (%v)", got, err)
	}
}

// storeKeyOf is the codebase key Create will use for a root.
func storeKeyOf(t *testing.T, _ *Store, root string) string {
	t.Helper()
	canonical, err := core.CanonicalizePath(root)
	if err != nil {
		t.Fatal(err)
	}
	return core.CodebaseKey(canonical)
}

// A book that already exists is inherited, not overwritten: only the parts of
// the shape that are missing are added.
func TestSeedBookKeepsWhatIsAlreadyWritten(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	root := t.TempDir()
	first, err := store.Create(root, "Winerim", "", "", false, Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(store.BookDir(first.CodebaseKey), ProjectFile)
	if err := os.WriteFile(project, []byte("# Real project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(store.BookDir(first.CodebaseKey), book.OwnerFile)); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(first.CodebaseKey); err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(root, "Winerim II", "", "", false, Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.ProjectIndex(second.CodebaseKey); got != "# Real project\n" {
		t.Fatalf("an existing index was overwritten: %q", got)
	}
	if store.OwnerPrefs(second.CodebaseKey) == "" {
		t.Fatal("the missing OWNER.md was not restored")
	}
}

// What an owner may do is the user's call (decision of 19/11): the role prompt
// describes the role, OWNER.md comes last and is stated as governing, and the
// prompt carries no built-in policy on deployments, releases or merges.
func TestRolePromptLetsOwnerMDGovern(t *testing.T) {
	prompt := RolePrompt("Winerim", "master", "Merge to master whenever tests pass.")
	prefsAt := strings.Index(prompt, "Merge to master whenever tests pass.")
	roleAt := strings.Index(prompt, "## What you decide")
	if prefsAt < 0 || roleAt < 0 || prefsAt < roleAt {
		t.Fatalf("OWNER.md is not placed after the role:\n%s", prompt)
	}
	if !strings.Contains(prompt, "This file governs over the role above;") {
		t.Fatal("the prompt does not state that OWNER.md governs")
	}
	flat := strings.Join(strings.Fields(prompt), " ")
	for _, gone := range []string{
		"What you never decide", "never override", "never covers a deployment",
		"Permissions, deployments, releases", "integration authorization",
		// Public code does not assume a company behind every project, nor
		// the section names of one user's private AGENTS.md.
		"company and its customers", "Ownership section",
	} {
		if strings.Contains(flat, gone) {
			t.Fatalf("the role prompt still carries the built-in policy %q", gone)
		}
	}
	for _, want := range []string{
		"Your instructions set what you decide and what you ask",
		"the user's AGENTS.md files, OWNER.md and the book's decisions", "ask with ask_user",
		"You cannot approve tool permissions",
		"People depend on this project",
	} {
		if !strings.Contains(flat, want) {
			t.Fatalf("role prompt missing %q", want)
		}
	}
	for _, want := range []string{
		"balance", "sessions tool", "work/", "book search first",
		"Read to understand; do not read to execute", "book-init",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("role prompt missing %q", want)
		}
	}
	if strings.Contains(RolePrompt("Winerim", "master", ""), "## The user's instructions for this project") {
		t.Fatal("an empty OWNER.md still produced a preferences section")
	}
}

// The balance is only what changed (the user's decision of 17/10): the owner
// acts on what is actionable first, then reports the delta in one or two
// lines, and reads what is stopped from its tools rather than from memory.
// Every report is read, decided and acted on — not merely mined for a delta.
func TestRolePromptGivesAnOperatingSequence(t *testing.T) {
	prompt := RolePrompt("Winerim", "master", "")
	actAt := strings.Index(prompt, "act on what is actionable before reporting")
	balanceAt := strings.Index(prompt, "report only what\nchanged since your last balance")
	if actAt < 0 || balanceAt < actAt {
		t.Fatalf("the operating sequence is not act → balance:\n%s", prompt)
	}
	for _, want := range []string{
		"one or two lines",
		"do not repeat an\nunchanged inventory",
		"read it, decide what it means for the project, and act",
		"not\nfrom memory",
		"no sheets under areas/",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("role prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "End EVERY turn") || strings.Contains(prompt, "Only\n  what is in the book survives") {
		t.Fatal("the role prompt still demands the per-turn ritual or claims only the book survives a restart")
	}
}

// The owner is told which branch areas/ describes, because a report carries a
// branch name and nothing in "feat/x" says whether it is the canonical one.
// Outside git the unknown is explicit rather than a guessed default.
func TestRolePromptNamesTheCanonicalRef(t *testing.T) {
	with := RolePrompt("Winerim", "master", "")
	if !strings.Contains(with, "Truth in areas/ is the canonical branch, `master`") {
		t.Fatalf("the canonical ref is not in the prompt:\n%s", with)
	}
	without := RolePrompt("Winerim", "", "")
	if !strings.Contains(without, "which is unknown here") {
		t.Fatalf("an unknown canonical ref was not stated:\n%s", without)
	}
	if strings.Contains(without, "canonical branch, `") {
		t.Fatal("an unknown canonical ref was rendered as a branch name")
	}
}

// The canonical ref is detected once, at creation, and stored: it is a
// property of the repository, and every report and prompt needs the same one.
func TestCanonicalRefIsDetectedAndStored(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "--initial-branch=master")
	git(t, repo, "config", "user.email", "t@example.com")
	git(t, repo, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "first")
	git(t, repo, "checkout", "-b", "feat/x")

	store := NewStore(t.TempDir())
	own, err := store.Create(repo, "Winerim", "", "", false, Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	if own.CanonicalRef != "master" {
		t.Fatalf("canonical ref = %q, want master even while on feat/x", own.CanonicalRef)
	}
	reloaded, found, err := store.FindByCodebase(own.CodebaseKey)
	if err != nil || !found {
		t.Fatalf("owner not reloaded: %v", err)
	}
	if reloaded.CanonicalRef != "master" {
		t.Fatalf("canonical ref was not persisted: %+v", reloaded)
	}
	// Outside a repository the position is unknown, and stays empty rather
	// than becoming a plausible "main".
	if got := DetectCanonicalRef(t.TempDir()); got != "" {
		t.Fatalf("a directory that is not a repository reported %q", got)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// Every child is told to end with the delta, because the owner cannot diff the
// book against work it never saw.
func TestBookSectionAsksForTheDelta(t *testing.T) {
	section := BookSection("Winerim", "# Project\n")
	if !strings.Contains(section, "## Book delta") {
		t.Fatalf("children are not asked for a book delta:\n%s", section)
	}
}

func TestOwnerPrefsAreCapped(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	own, err := store.Create(t.TempDir(), "Winerim", "", "", false, Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("x", MaxOwnerPrefsBytes+500)
	if err := os.WriteFile(filepath.Join(store.BookDir(own.CodebaseKey), book.OwnerFile), []byte(huge), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := len(store.OwnerPrefs(own.CodebaseKey)); got != MaxOwnerPrefsBytes {
		t.Fatalf("OWNER.md contributed %d bytes", got)
	}
}

func TestRolePromptSaysWhenToSendToAnotherOwner(t *testing.T) {
	prompt := RolePrompt("Winerim", "master", "")
	for _, want := range []string{"action send", "judge it with your book", "action comment"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("role prompt lacks %q", want)
		}
	}
}

// The book section must not reward skipping the code: a sheet that is wrong
// about how something works would otherwise be echoed back by every session
// (decisions/owner-derecho-a-mirar.md left this cut for a separate step).
func TestBookSectionDoesNotRankTheBookAboveTheCode(t *testing.T) {
	section := BookSection("Winerim", "# Project\n")
	if strings.Contains(section, "faster than reading the code") || strings.Contains(section, "compare your work against") {
		t.Fatalf("the book section still discourages reading the code:\n%s", section)
	}
	if !strings.Contains(section, "read its sheet with the book tool") {
		t.Fatal("the book section no longer asks to read the sheet first")
	}
}

// Books seeded before 19/11 start with a header calling OWNER.md preferences
// that "never override the owner's own rules", which contradicts the prompt
// that now says the file governs. The exact historic headers are dropped on
// read; the user's file on disk is left untouched.
func TestOwnerPrefsDropTheLegacyTemplateHeader(t *testing.T) {
	legacy := []string{
		`# The user's preferences for this project

Edit this file yourself. The owner reads it on every turn and cannot write it.
It carries preferences — how to brief sessions, what to escalate, tone,
conventions — not permissions: it never overrides the owner's own rules.
`,
		`# The user's preferences for this project

Edit this file yourself. The owner cannot write it; it rereads it when its
session starts, reloads or compacts, not on every turn.
It carries preferences — how to brief sessions, what to escalate, tone,
conventions — not permissions: it never overrides the owner's own rules.
`,
	}
	store := NewStore(t.TempDir())
	own, err := store.Create(t.TempDir(), "Winerim", "", "", false, Avatar{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.BookDir(own.CodebaseKey), book.OwnerFile)
	for i, header := range legacy {
		for _, tc := range []struct{ file, want string }{
			{header, ""},
			{header + "\nMerge to master whenever tests pass.\n", "Merge to master whenever tests pass."},
			{strings.ReplaceAll(header+"\nDeploy on green.\n", "\n", "\r\n"), "Deploy on green."},
		} {
			if err := os.WriteFile(path, []byte(tc.file), 0o600); err != nil {
				t.Fatal(err)
			}
			got := store.OwnerPrefs(own.CodebaseKey)
			if strings.TrimSpace(got) != tc.want {
				t.Errorf("legacy header %d: OwnerPrefs = %q, want %q", i, got, tc.want)
			}
			if strings.Contains(RolePrompt("Winerim", "master", got), "never overrides the owner's own rules") {
				t.Errorf("legacy header %d still reaches the role prompt", i)
			}
			if onDisk, _ := os.ReadFile(path); string(onDisk) != tc.file {
				t.Fatalf("OwnerPrefs rewrote the user's file")
			}
		}
	}
	// The same words written by the user anywhere but as the seeded header stay.
	quoted := "My rules.\n\n" + legacy[0]
	if err := os.WriteFile(path, []byte(quoted), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := store.OwnerPrefs(own.CodebaseKey); got != quoted {
		t.Fatalf("text that is not the seeded header was stripped: %q", got)
	}
}

// Public docs describe OWNER.md as the user's instructions, and the owner as
// answering what the book or the user's instructions let it decide.
func TestDocsDescribeOwnerMDAsInstructions(t *testing.T) {
	for file, stale := range map[string]string{
		"../../docs/overview.md": "answers what the book already answers",
		"../../docs/owners.md":   "your preferences for the owner",
	} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), stale) {
			t.Errorf("%s still says %q", file, stale)
		}
	}
}
