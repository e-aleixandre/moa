package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/owner"
)

// writeBookIndex replaces the owner's PROJECT.md.
func writeBookIndex(t *testing.T, codebaseKey, content string) {
	t.Helper()
	store, err := owner.Default()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.BookDir(codebaseKey), owner.ProjectFile), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func systemPromptOf(t *testing.T, sess *ManagedSession) string {
	t.Helper()
	return sess.runtime.Context().Agent.SystemPrompt()
}

func TestChildSessionPromptCarriesTheBook(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()

	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: root, Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	writeBookIndex(t, info.CodebaseKey, "# Project\n\n2026-08-01 vale-basta closed\n")

	child, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	prompt := systemPromptOf(t, child)
	for _, want := range []string{"## Project book", "Winerim", "vale-basta closed", "ask_user"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("child prompt missing %q:\n%s", want, prompt)
		}
	}
	// A child reads the book; the owner's role belongs to the owner alone.
	if strings.Contains(prompt, "You are the owner of this project") {
		t.Fatal("child prompt carries the owner role")
	}
}

func TestOwnerSessionPromptCarriesRoleAndBook(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)

	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: t.TempDir(), Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	sess, ok := mgr.Get(info.SessionID)
	if !ok {
		t.Fatal("owner session missing")
	}
	prompt := systemPromptOf(t, sess)
	if !strings.Contains(prompt, "You are the owner of this project") {
		t.Fatalf("owner prompt missing its role:\n%s", prompt)
	}
	if !strings.Contains(prompt, "## Project book") {
		t.Fatalf("owner prompt missing the book:\n%s", prompt)
	}
}

func TestSessionWithoutOwnerHasNoBookSection(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)

	sess, err := mgr.CreateSession(CreateOpts{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(systemPromptOf(t, sess), "## Project book") {
		t.Fatal("a codebase without an owner injected a book section")
	}
}

func TestBookIndexIsCappedInThePrompt(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()

	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: root, Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	marker := "OVER-THE-CAP"
	writeBookIndex(t, info.CodebaseKey, strings.Repeat("x", owner.MaxProjectBytes)+marker)

	child, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(systemPromptOf(t, child), marker) {
		t.Fatal("PROJECT.md past the cap reached the prompt")
	}
}

func TestReloadPicksUpAnEditedBook(t *testing.T) {
	ctx := context.Background()
	mgr := newOwnerTestManager(t, ctx)
	root := t.TempDir()

	info, err := mgr.CreateOwner(CreateOwnerOpts{Root: root, Name: "Winerim"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := mgr.CreateSession(CreateOpts{CWD: root})
	if err != nil {
		t.Fatal(err)
	}
	writeBookIndex(t, info.CodebaseKey, "# Project\n\nfreshly-written-line\n")

	changed := child.reloadSession()
	if !contains(changed, "Project book") {
		t.Fatalf("reload did not report the book as changed: %v", changed)
	}
	// The reload must reach the live agent's prompt, not just the holder.
	if !strings.Contains(systemPromptOf(t, child), "freshly-written-line") {
		t.Fatal("reloaded prompt does not carry the edited book")
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
