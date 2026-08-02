package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/session"
)

// Remove in 1.0 together with migrate_codebase.go.

func gitOrSkip(t *testing.T, dir string) func(args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	return func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// newRepo creates a repository with one commit so `git worktree add` works.
func newRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := gitOrSkip(t, dir)
	git("init")
	git("config", "user.email", "t@t.t")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "init")
}

// writeLegacyFact plants a fact in the path-keyed store a given cwd used to
// have, the way an older moa left it on disk.
func writeLegacyFact(t *testing.T, configDir, cwd string, m Memory) string {
	t.Helper()
	dir := legacyMemoryDir(t, configDir, cwd)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, m.Name+".md")
	if err := os.WriteFile(path, serialize(m), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func legacyMemoryDir(t *testing.T, configDir, cwd string) string {
	t.Helper()
	return filepath.Join(New(configDir, cwd).legacyProjectRoot, "memory")
}

// writeSession fakes what the session store persists for a session in cwd:
// the migration reads the cwd back out of this metadata to learn which
// path-keyed store belonged to which directory.
func writeSession(t *testing.T, configDir, cwd string, updated time.Time) {
	t.Helper()
	dir := filepath.Join(configDir, "sessions", "scope_"+strings.NewReplacer("/", "_", ".", "_", " ", "_").Replace(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef01234567"
	sess := map[string]any{
		"id":       id,
		"version":  2,
		"created":  updated,
		"updated":  updated,
		"metadata": map[string]any{"cwd": cwd},
	}
	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// failToRead makes one source fact unreadable until the returned function is
// called, and stands in for any failure midway through the copy.
//
// It injects the error instead of chmodding the file to 000 because these
// tests also run as root — in CI containers and on this machine — and root
// reads a mode-000 file happily, so the chmod version silently skipped itself
// exactly where the behaviour it covers matters. The failure is injected at
// the same place a real IO error surfaces, so what is exercised is the
// production path, not a test-only branch.
func failToRead(t *testing.T, path string) func() {
	t.Helper()
	prev := readFactFile
	readFactFile = func(name string) ([]byte, error) {
		if name == path {
			return nil, fmt.Errorf("open %s: %w", name, fs.ErrPermission)
		}
		return prev(name)
	}
	restore := func() { readFactFile = prev }
	t.Cleanup(restore)
	return restore
}

// corruptSessions truncates every session file mid-JSON, the way a machine
// that lost power during a write leaves them.
func corruptSessions(t *testing.T, configDir string) {
	t.Helper()
	stores, err := os.ReadDir(filepath.Join(configDir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range stores {
		files, err := os.ReadDir(filepath.Join(configDir, "sessions", d.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			path := filepath.Join(configDir, "sessions", d.Name(), f.Name())
			if err := os.WriteFile(path, []byte(`{"id": "0123456789abcdef0123`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestWorktreesOfOneRepoShareMemory(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)
	wt := filepath.Join(base, "wt")
	gitOrSkip(t, repo)("worktree", "add", "-b", "one", wt)

	configDir := t.TempDir()
	main := New(configDir, repo)
	if err := main.Write(Memory{Name: "uses-docker", Description: "d", Type: TypeProject, Body: "b"}); err != nil {
		t.Fatal(err)
	}

	// A worktree is a different path, so under the old keying this store would
	// be empty. It is the same repository, so it is the same memory.
	other := New(configDir, wt)
	if got := other.List(); len(got) != 1 || got[0].Name != "uses-docker" {
		t.Fatalf("a worktree should read the repository's memory, got %+v", got)
	}
}

func TestDistinctReposDoNotShareMemory(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	newRepo(t, a)
	newRepo(t, b)

	configDir := t.TempDir()
	if err := New(configDir, a).Write(Memory{Name: "secret", Description: "d", Type: TypeProject, Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if got := New(configDir, b).List(); len(got) != 0 {
		t.Fatalf("an unrelated repository must not see these facts, got %+v", got)
	}
}

func TestMigrateCopiesAndKeepsLegacyStore(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	legacy := writeLegacyFact(t, configDir, repo, Memory{Name: "learned", Description: "d", Type: TypeProject, Body: "body"})
	writeSession(t, configDir, repo, time.Now())

	s := New(configDir, repo)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}

	m, ok, err := s.Read("project/learned")
	if err != nil || !ok {
		t.Fatalf("fact not migrated: ok=%v err=%v", ok, err)
	}
	if m.Body != "body" {
		t.Errorf("body lost in migration: %q", m.Body)
	}
	// Facts are the user's private notes; the migrated directory must not be
	// looser than the one Write creates.
	info, err := os.Stat(s.ProjectDir())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("migrated store mode = %o, want 700", perm)
	}
	// Non-destructive on purpose: an older binary must still find its memory.
	if !fileExists(legacy) {
		t.Error("the legacy store must be left untouched, so a downgrade still works")
	}
}

func TestMigrateFindsStoreOfADifferentWorktree(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)
	wt := filepath.Join(base, "wt")
	gitOrSkip(t, repo)("worktree", "add", "-b", "one", wt)

	configDir := t.TempDir()
	// The facts were learned in the worktree; the session in it is the only
	// record of which path-keyed store that was.
	writeLegacyFact(t, configDir, wt, Memory{Name: "from-worktree", Description: "d", Type: TypeProject, Body: "b"})
	writeSession(t, configDir, wt, time.Now())

	// Migrating from the main checkout, which never had a store of its own.
	s := New(configDir, repo)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/from-worktree"); !ok {
		t.Fatal("a sibling worktree's memory belongs to the same codebase")
	}
}

func TestMigrateIgnoresOtherCodebases(t *testing.T) {
	base := t.TempDir()
	mine := filepath.Join(base, "mine")
	theirs := filepath.Join(base, "theirs")
	newRepo(t, mine)
	newRepo(t, theirs)

	configDir := t.TempDir()
	writeLegacyFact(t, configDir, mine, Memory{Name: "mine", Description: "d", Type: TypeProject, Body: "b"})
	writeLegacyFact(t, configDir, theirs, Memory{Name: "theirs", Description: "d", Type: TypeProject, Body: "b"})
	writeSession(t, configDir, mine, time.Now())
	writeSession(t, configDir, theirs, time.Now())

	s := New(configDir, mine)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/theirs"); ok {
		t.Fatal("another repository's memory must not be pulled in")
	}
	if _, ok, _ := s.Read("project/mine"); !ok {
		t.Fatal("this repository's memory should have been migrated")
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	writeLegacyFact(t, configDir, repo, Memory{Name: "learned", Description: "d", Type: TypeProject, Body: "b"})
	writeSession(t, configDir, repo, time.Now())

	s := New(configDir, repo)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	// A fact deleted after migrating must stay deleted: a second run that
	// copied again would resurrect it, and the user would delete it forever.
	if err := s.Delete("project/learned"); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("a second migration must not copy anything again, got %+v", got)
	}
}

func TestMigrateStillCopiesWhenAnEmptyDestinationExists(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	writeLegacyFact(t, configDir, repo, Memory{Name: "learned", Description: "d", Type: TypeProject, Body: "b"})

	s := New(configDir, repo)
	// The destination directory alone proves nothing: an ordinary memory.write
	// creates it, and so does a migration that died before copying a thing.
	// Treating it as "already migrated" would abandon the legacy store forever.
	if err := os.MkdirAll(s.ProjectDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/learned"); !ok {
		t.Fatal("an empty destination must not seal the migration")
	}
}

func TestMigrateAfterAFailureFollowedByAWrite(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	legacyDir := legacyMemoryDir(t, configDir, repo)
	writeLegacyFact(t, configDir, repo, Memory{Name: "learned", Description: "d", Type: TypeProject, Body: "b"})
	unreadable := filepath.Join(legacyDir, "unreadable.md")
	if err := os.WriteFile(unreadable, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stopFailing := failToRead(t, unreadable)

	s := New(configDir, repo)
	if err := s.Migrate(); err == nil {
		t.Fatal("a failed copy must be reported")
	}
	// The exact sequence that used to strand the legacy store: the migration
	// fails, the session carries on, and the first thing the agent remembers
	// creates the destination directory.
	if err := s.Write(Memory{Name: "written-after", Description: "d", Type: TypeProject, Body: "b"}); err != nil {
		t.Fatal(err)
	}
	stopFailing()
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/learned"); !ok {
		t.Fatal("the retry must still find the legacy store")
	}
	if _, ok, _ := s.Read("project/written-after"); !ok {
		t.Fatal("the merge must not throw away what was written meanwhile")
	}
}

func TestMigrateNeverOverwritesADestinationFact(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	writeLegacyFact(t, configDir, repo, Memory{Name: "shared", Description: "d", Type: TypeProject, Body: "legacy text"})

	s := New(configDir, repo)
	// Whatever is in the destination is live: an ID in an earlier conversation
	// resolves to it, so the copy has to move aside, not the other way round.
	if err := s.Write(Memory{Name: "shared", Description: "d", Type: TypeProject, Body: "current text"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	m, ok, err := s.Read("project/shared")
	if err != nil || !ok {
		t.Fatalf("shared missing: ok=%v err=%v", ok, err)
	}
	if m.Body != "current text" {
		t.Errorf("the destination fact was overwritten: %q", m.Body)
	}
	other, ok, _ := s.Read("project/shared-2")
	if !ok || other.Body != "legacy text" {
		t.Errorf("the legacy version must be kept beside it, got ok=%v %+v", ok, other)
	}
	// And re-running must not file the same bytes again under "shared-3".
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/shared-3"); ok {
		t.Error("a second run duplicated a fact it had already copied")
	}
}

func TestMigrateMergesSiblingWorktreesWithoutLosingFacts(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)
	git := gitOrSkip(t, repo)
	wt1 := filepath.Join(base, "wt1")
	wt2 := filepath.Join(base, "wt2")
	git("worktree", "add", "-b", "one", wt1)
	git("worktree", "add", "-b", "two", wt2)

	configDir := t.TempDir()
	// Disjoint facts merge; the shared name is where data could be lost.
	writeLegacyFact(t, configDir, wt1, Memory{Name: "only-one", Description: "d", Type: TypeProject, Body: "one"})
	writeLegacyFact(t, configDir, wt2, Memory{Name: "only-two", Description: "d", Type: TypeProject, Body: "two"})
	older := writeLegacyFact(t, configDir, wt1, Memory{Name: "shared", Description: "d", Type: TypeProject, Body: "older text"})
	newer := writeLegacyFact(t, configDir, wt2, Memory{Name: "shared", Description: "d", Type: TypeProject, Body: "newer text"})
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	writeSession(t, configDir, wt1, time.Now().Add(-time.Minute))
	writeSession(t, configDir, wt2, time.Now())

	s := New(configDir, repo)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"project/only-one", "project/only-two"} {
		if _, ok, _ := s.Read(id); !ok {
			t.Errorf("%s lost in the merge", id)
		}
	}
	// The newest text keeps the name every past conversation refers to.
	m, ok, err := s.Read("project/shared")
	if err != nil || !ok {
		t.Fatalf("shared fact missing: ok=%v err=%v", ok, err)
	}
	if m.Body != "newer text" {
		t.Errorf("the most recent version should keep the name, got %q", m.Body)
	}
	// And the one it displaced is still there, under a suffixed name.
	other, ok, err := s.Read("project/shared-2")
	if err != nil || !ok {
		t.Fatalf("the displaced version must be kept: ok=%v err=%v", ok, err)
	}
	if other.Body != "older text" {
		t.Errorf("displaced fact has the wrong body: %q", other.Body)
	}
	// The suffixed file's frontmatter must agree with its filename, or every
	// later List warns about a mismatch this migration introduced.
	raw, err := os.ReadFile(filepath.Join(s.ProjectDir(), "shared-2.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "name: shared-2") {
		t.Errorf("frontmatter name not rewritten: %s", raw)
	}
}

func TestMigrateCollapsesIdenticalFacts(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)
	git := gitOrSkip(t, repo)
	wt := filepath.Join(base, "wt")
	git("worktree", "add", "-b", "one", wt)

	configDir := t.TempDir()
	// The same fact learned in two worktrees is the common case, not a
	// conflict: it must not become "same" and "same-2".
	fact := Memory{Name: "same", Description: "d", Type: TypeProject, Body: "identical"}
	writeLegacyFact(t, configDir, repo, fact)
	writeLegacyFact(t, configDir, wt, fact)
	writeSession(t, configDir, repo, time.Now())
	writeSession(t, configDir, wt, time.Now())

	s := New(configDir, repo)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if got := s.List(); len(got) != 1 {
		t.Fatalf("identical facts should collapse into one, got %+v", got)
	}
}

func TestMigrateWithoutSessionsStillMovesOwnStore(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	// No session history at all (pruned, or a fresh config): the store of the
	// directory being opened is still known by construction.
	writeLegacyFact(t, configDir, repo, Memory{Name: "learned", Description: "d", Type: TypeProject, Body: "b"})

	s := New(configDir, repo)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/learned"); !ok {
		t.Fatal("this workspace's own store must migrate without a session to point at it")
	}
}

func TestMigrateInterruptedPublishesNoFacts(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	legacyDir := legacyMemoryDir(t, configDir, repo)
	writeLegacyFact(t, configDir, repo, Memory{Name: "learned", Description: "d", Type: TypeProject, Body: "b"})
	// A fact that cannot be read stands in for any failure midway through:
	// a full disk, a crash, the process being killed.
	unreadable := filepath.Join(legacyDir, "unreadable.md")
	if err := os.WriteFile(unreadable, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stopFailing := failToRead(t, unreadable)

	s := New(configDir, repo)
	if err := s.Migrate(); err == nil {
		t.Fatal("a failed copy must be reported, not silently partial")
	}
	// No marker, so nothing claims this codebase is done; and nothing was
	// copied either, since collection reads every source before the first
	// write.
	if fileExists(s.migrationRecordPath()) {
		t.Error("a failed migration must not leave a marker behind")
	}
	if got := s.List(); len(got) != 0 {
		t.Errorf("an aborted collection must publish nothing, got %+v", got)
	}

	// And the retry, once the obstacle is gone, completes normally.
	stopFailing()
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/learned"); !ok {
		t.Fatal("the retry should have migrated everything")
	}
}

func TestMigrateCountMatchesWhatIsOnDisk(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)
	git := gitOrSkip(t, repo)
	wt := filepath.Join(base, "wt")
	git("worktree", "add", "-b", "one", wt)

	configDir := t.TempDir()
	// The collision that used to lose a fact without anybody noticing: two
	// versions of "foo" displace one to "foo-2", and an unrelated fact really
	// called "foo-2" then landed on top of it. Three facts in, three facts out.
	old := writeLegacyFact(t, configDir, repo, Memory{Name: "foo", Description: "d", Type: TypeProject, Body: "older foo"})
	newer := writeLegacyFact(t, configDir, wt, Memory{Name: "foo", Description: "d", Type: TypeProject, Body: "newer foo"})
	writeLegacyFact(t, configDir, wt, Memory{Name: "foo-2", Description: "d", Type: TypeProject, Body: "an unrelated fact"})
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	writeSession(t, configDir, wt, time.Now())

	s := New(configDir, repo)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}

	bodies := map[string]string{}
	for _, m := range s.List() {
		bodies[m.Name] = m.Body
	}
	if len(bodies) != 3 {
		t.Fatalf("three distinct facts went in, %d came out: %v", len(bodies), bodies)
	}
	if bodies["foo"] != "newer foo" {
		t.Errorf("the newest version should keep the name, got %q", bodies["foo"])
	}
	if bodies["foo-2"] != "an unrelated fact" {
		t.Errorf("a fact genuinely named foo-2 must keep its own name, got %q", bodies["foo-2"])
	}
	if bodies["foo-3"] != "older foo" {
		t.Errorf("the displaced version should move to the next free name, got %q", bodies["foo-3"])
	}
}

func TestMigrateIgnoresLeftoverStagingDir(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	writeLegacyFact(t, configDir, repo, Memory{Name: "learned", Description: "d", Type: TypeProject, Body: "b"})

	s := New(configDir, repo)
	// What an older build left behind between its staging write and its
	// rename. It is not the destination and it is not the marker, so it must
	// neither block the migration nor be adopted into it.
	if err := os.MkdirAll(s.codebaseRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stale, err := os.MkdirTemp(s.codebaseRoot, ".memory-migrating-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "half.md"), serialize(Memory{Name: "half", Description: "d", Type: TypeProject, Body: "b"}), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/learned"); !ok {
		t.Fatal("a leftover staging directory must not block the migration")
	}
	if _, ok, _ := s.Read("project/half"); ok {
		t.Fatal("a half-written staging directory must not be adopted")
	}
}

func TestMigrateNoLegacyStores(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	s := New(t.TempDir(), repo)
	if err := s.Migrate(); err != nil {
		t.Fatalf("a fresh install must be a clean no-op: %v", err)
	}
	// Nothing to migrate writes nothing: a marker per codebase to state the
	// obvious would create a directory for every repository the user opens.
	if dirExists(s.codebaseRoot) {
		t.Error("nothing to migrate should leave no directory behind")
	}
}

func TestMigrateRunsBeforeV1Wrapping(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	s := New(configDir, repo)
	// A v1 MEMORY.md beside a v2 store: both migrations have work to do, and
	// wrapping the flat file first would create the destination directory and
	// convince the codebase migration it had already run.
	writeLegacyFact(t, configDir, repo, Memory{Name: "learned", Description: "d", Type: TypeProject, Body: "b"})
	if err := os.MkdirAll(s.legacyProjectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.legacyProjectRoot, "MEMORY.md"), []byte("flat notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/learned"); !ok {
		t.Error("the path-keyed store was masked by the v1 migration")
	}
	if _, ok, _ := s.Read("project/notas-legado-v1"); !ok {
		t.Error("the v1 flat file should still have been wrapped")
	}
}

func TestMigrateSkipsStoresOfDeletedWorktrees(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	// A worktree that no longer exists: git cannot say which repository it
	// belonged to, and guessing from its parent directory would merge
	// unrelated projects. Its store stays where it is.
	gone := filepath.Join(base, "gone")
	writeLegacyFact(t, configDir, gone, Memory{Name: "orphan", Description: "d", Type: TypeProject, Body: "b"})
	writeSession(t, configDir, gone, time.Now())
	writeLegacyFact(t, configDir, repo, Memory{Name: "alive", Description: "d", Type: TypeProject, Body: "b"})

	s := New(configDir, repo)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/orphan"); ok {
		t.Error("a vanished directory must not be attributed to this codebase")
	}
	if !fileExists(filepath.Join(legacyMemoryDir(t, configDir, gone), "orphan.md")) {
		t.Error("the orphaned store must be left intact")
	}
	// It is written down, with the numbers: that is the only thing making
	// those facts findable by hand later.
	ledger := s.readOrphanLedger()
	if len(ledger.Stores) != 1 || ledger.Stores[0].Facts != 1 {
		t.Fatalf("the orphaned store should be listed with its fact count, got %+v", ledger.Stores)
	}
	if ledger.Stores[0].LastKnownCWD != gone {
		t.Errorf("the last known path should be recorded, got %q", ledger.Stores[0].LastKnownCWD)
	}
	// But it is not this codebase's unfinished business: the vanished
	// directory is outside this repository, so no session in it could ever
	// make it ours. Holding the marker open for it would make every start of
	// every project rescan the session history for good.
	rec, ok := s.readMigrationRecord()
	if !ok {
		t.Fatal("a marker should have been written")
	}
	if !rec.Complete {
		t.Error("an orphan no session of this codebase can claim must not block the seal")
	}
	if len(rec.Pending) != 0 {
		t.Errorf("an unrelated orphan must not be pending for us, got %+v", rec.Pending)
	}

	// A sealed migration still must not copy anything twice.
	if err := s.Delete("project/alive"); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if got := s.List(); len(got) != 0 {
		t.Errorf("a store already copied must not be copied again, got %+v", got)
	}
}

func TestMigrateKeepsWatchingAVanishedWorktreeOfThisRepo(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)
	git := gitOrSkip(t, repo)
	// A worktree of THIS repository, removed after learning something. Its
	// last known path is inside the repository, so if it ever comes back it
	// can only come back as ours: that is worth one stat per start.
	wt := filepath.Join(repo, "wt")
	git("worktree", "add", "-b", "one", wt)

	configDir := t.TempDir()
	writeLegacyFact(t, configDir, wt, Memory{Name: "from-gone-wt", Description: "d", Type: TypeProject, Body: "b"})
	writeSession(t, configDir, wt, time.Now())
	git("worktree", "remove", "--force", wt)

	s := New(configDir, repo)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	rec, _ := s.readMigrationRecord()
	if len(rec.Pending) != 1 || rec.Pending[0].CWD != wt {
		t.Fatalf("a vanished worktree of this repo should stay pending for us, got %+v", rec.Pending)
	}
	if !s.mustLookAgain(rec) == dirExists(wt) {
		t.Errorf("while the directory is gone there is nothing to look at again")
	}

	// Recreated: the next start notices with a stat, and the facts arrive.
	git("worktree", "add", wt, "one")
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/from-gone-wt"); !ok {
		t.Error("a returning worktree's store should finally be copied")
	}
	rec, _ = s.readMigrationRecord()
	if len(rec.Pending) != 0 {
		t.Errorf("once copied it is not pending any more, got %+v", rec.Pending)
	}
}

func TestMigrateClaimsItsOwnStoreAfterAnotherWorktreeSealedTheCodebase(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)
	wt := filepath.Join(base, "wt")
	gitOrSkip(t, repo)("worktree", "add", "-b", "one", wt)

	configDir := t.TempDir()
	// The worktree's store exists, but nothing on disk says whose it is: no
	// session ever recorded that path. Migrating from the main checkout seals
	// the codebase without it.
	writeLegacyFact(t, configDir, wt, Memory{Name: "from-wt", Description: "d", Type: TypeProject, Body: "b"})
	writeLegacyFact(t, configDir, repo, Memory{Name: "from-repo", Description: "d", Type: TypeProject, Body: "b"})
	if err := New(configDir, repo).Migrate(); err != nil {
		t.Fatal(err)
	}

	// Opening the worktree itself is the evidence that was missing: its store
	// is its own by construction. A seal written by a sibling must not lock it
	// out — this is the path that turns an orphan back into a migrated fact.
	w := New(configDir, wt)
	if err := w.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := w.Read("project/from-wt"); !ok {
		t.Fatal("opening the worktree should have claimed its own store")
	}
	// And it stops being listed as an orphan.
	for _, e := range w.readOrphanLedger().Stores {
		if e.LastKnownCWD == wt || e.Hash == filepath.Base(w.legacyProjectRoot) {
			t.Errorf("a store that has been copied must leave the orphan list, got %+v", e)
		}
	}
}

func TestMigrateSealedStartDoesNoWork(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	writeLegacyFact(t, configDir, repo, Memory{Name: "learned", Description: "d", Type: TypeProject, Body: "b"})
	// Somebody else's orphan, in a directory unrelated to this repository. It
	// used to keep every codebase on the expensive path forever.
	gone := filepath.Join(base, "gone")
	writeLegacyFact(t, configDir, gone, Memory{Name: "orphan", Description: "d", Type: TypeProject, Body: "b"})
	writeSession(t, configDir, gone, time.Now())

	s := New(configDir, repo)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}

	// The sealed path must not walk the session history again. Sessions are
	// the expensive part — one open and decode per file, thousands of them on
	// an old install — so making them unreadable is a load-bearing assertion:
	// a run that still scanned them would report itself blinded and reopen the
	// marker.
	if err := os.RemoveAll(filepath.Join(configDir, "sessions")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.migrationRecordPath())
	if err != nil {
		t.Fatal(err)
	}
	entered := false
	migrationInsideLock = func() func() { entered = true; return func() {} }
	t.Cleanup(func() { migrationInsideLock = nil })
	if err := New(configDir, repo).Migrate(); err != nil {
		t.Fatal(err)
	}
	if entered {
		t.Error("a sealed migration took the lock and redid the scan")
	}
	after, err := os.ReadFile(s.migrationRecordPath())
	if err != nil {
		t.Fatal(err)
	}
	// Untouched bytes prove the run returned before the lock, the scan and
	// the write: everything after the early return rewrites this file.
	if !bytes.Equal(before, after) {
		t.Errorf("a sealed migration rewrote its marker:\n%s\n%s", before, after)
	}
}

func TestMigrationRecordIsValidatedBeforeItSeals(t *testing.T) {
	cases := []struct {
		name   string
		marker string
	}{
		// The one that lost facts: syntactically valid, semantically empty,
		// and it sealed the migration on its own word.
		{"complete without anything else", `{"complete":true}`},
		{"a version this build cannot read", `{"version":999,"updated_at":"2026-01-01T00:00:00Z","complete":true}`},
		{"the right version but no timestamp", `{"version":1,"complete":true}`},
		{"an entry that is not a store name", `{"version":1,"updated_at":"2026-01-01T00:00:00Z","complete":true,"copied_stores":[{"hash":"../elsewhere","facts":1}]}`},
		{"an entry with no hash at all", `{"version":1,"updated_at":"2026-01-01T00:00:00Z","complete":true,"copied_stores":[{"facts":1}]}`},
		// The baseline: a marker that is merely unparseable was always
		// retried, and must keep being retried.
		{"not json at all", `{bad`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := t.TempDir()
			repo := filepath.Join(base, "repo")
			newRepo(t, repo)

			configDir := t.TempDir()
			writeLegacyFact(t, configDir, repo, Memory{Name: "must-survive", Description: "d", Type: TypeProject, Body: "b"})

			s := New(configDir, repo)
			if err := os.MkdirAll(s.codebaseRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.migrationRecordPath(), []byte(c.marker), 0o600); err != nil {
				t.Fatal(err)
			}

			if err := s.Migrate(); err != nil {
				t.Fatal(err)
			}
			if _, ok, _ := s.Read("project/must-survive"); !ok {
				t.Fatal("a marker that cannot be trusted must not seal the migration")
			}
			// And the record it leaves is one this build can read back.
			rec, ok := s.readMigrationRecord()
			if !ok || !rec.Complete || len(rec.Copied) != 1 {
				t.Errorf("the rewritten marker should be usable and complete, got ok=%v %+v", ok, rec)
			}
		})
	}
}

func TestMigrateDistrustsARecordThatUndercountsAStore(t *testing.T) {
	// The dangerous marker is not the malformed one — it is the well-formed one
	// that names a real store and understates it. It passes every structural
	// check, so the only thing that can catch it is the store itself: a source
	// counted as smaller than what is on disk has to be read again. The same
	// path covers an older binary appending to a store already copied.
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	writeLegacyFact(t, configDir, repo, Memory{Name: "must-survive", Description: "d", Type: TypeProject, Body: "b"})

	s := New(configDir, repo)
	if err := os.MkdirAll(s.codebaseRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	ownHash := filepath.Base(s.legacyProjectRoot)
	marker := fmt.Sprintf(
		`{"version":1,"updated_at":"2026-01-01T00:00:00Z","complete":true,"copied_stores":[{"hash":%q,"facts":0}]}`,
		ownHash)
	if err := os.WriteFile(s.migrationRecordPath(), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/must-survive"); !ok {
		t.Fatal("a record claiming fewer facts than the store holds must not strand the rest")
	}
}

func TestMigrateSealsDespiteSessionsThatNeverRecordedACWD(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	writeLegacyFact(t, configDir, repo, Memory{Name: "learned", Description: "d", Type: TypeProject, Body: "b"})
	// A session written by a moa old enough not to store a cwd. It decodes
	// perfectly and will answer the same forever, so waiting for it to say
	// something would keep this codebase rescanning on every start for good.
	// On the real config here there are six of these.
	dir := filepath.Join(configDir, "sessions", "scope_old")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := map[string]any{
		"id":       "0123456789abcdef01234567",
		"version":  2,
		"updated":  time.Now(),
		"metadata": map[string]any{"model": "x"},
	}
	data, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "0123456789abcdef01234567.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(configDir, repo)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	rec, _ := s.readMigrationRecord()
	if !rec.Complete {
		t.Error("a session that can never name a directory must not hold the migration open")
	}
}

func TestMigrationRecordValidation(t *testing.T) {
	stamped := func(r migrationRecord) migrationRecord {
		r.Version = migrationRecordVersion
		r.UpdatedAt = time.Now().UTC()
		return r
	}
	// A real store name: 16 hex characters, the shape ProjectHash emits.
	const realHash = "bb1e830dec0e4654"
	if err := stamped(migrationRecord{Complete: true, Copied: []storeEntry{{Hash: realHash, Facts: 2}}}).validate(); err != nil {
		t.Errorf("a well-formed record was rejected: %v", err)
	}
	for name, rec := range map[string]migrationRecord{
		"no version":       {Complete: true, UpdatedAt: time.Now()},
		"future version":   {Version: 999, Complete: true, UpdatedAt: time.Now()},
		"no timestamp":     {Version: migrationRecordVersion, Complete: true},
		"traversal hash":   stamped(migrationRecord{Copied: []storeEntry{{Hash: "../x"}}}),
		"dot hash":         stamped(migrationRecord{Copied: []storeEntry{{Hash: ".."}}}),
		"empty hash":       stamped(migrationRecord{Pending: []storeEntry{{CWD: "/tmp"}}}),
		"negative facts":   stamped(migrationRecord{Copied: []storeEntry{{Hash: realHash, Facts: -1}}}),
		"nested separator": stamped(migrationRecord{Copied: []storeEntry{{Hash: "a/b"}}}),
		// A separator on the other platform: rejected here too, because the
		// shape is checked rather than the separator of whoever is running.
		"foreign separator": stamped(migrationRecord{Copied: []storeEntry{{Hash: `a\b`}}}),
		"not hexadecimal":   stamped(migrationRecord{Copied: []storeEntry{{Hash: "zz1e830dec0e4654"}}}),
		"too short":         stamped(migrationRecord{Copied: []storeEntry{{Hash: "abc123"}}}),
	} {
		if err := rec.validate(); err == nil {
			t.Errorf("%s: should have been rejected", name)
		}
	}
}

func TestMigrateReportsStoresNoSessionCanName(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	writeLegacyFact(t, configDir, repo, Memory{Name: "alive", Description: "d", Type: TypeProject, Body: "b"})
	// A store whose sessions were pruned: nothing on disk says which directory
	// it came from, so nobody can claim it and nobody can be told to look
	// again either. It has to survive in the one list that mentions it.
	unknown := filepath.Join(base, "unknown")
	writeLegacyFact(t, configDir, unknown, Memory{Name: "nameless", Description: "d", Type: TypeProject, Body: "b"})

	s := New(configDir, repo)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	ledger := s.readOrphanLedger()
	if len(ledger.Stores) != 1 || ledger.Stores[0].Facts != 1 {
		t.Fatalf("the unattributable store should be listed, got %+v", ledger.Stores)
	}
	if ledger.Stores[0].Path == "" || ledger.Note == "" {
		t.Errorf("the list must say where the files are and what to do, got %+v", ledger)
	}
	rec, _ := s.readMigrationRecord()
	if !rec.Complete {
		t.Error("a store nobody can ever attribute is not this codebase's unfinished business")
	}
}

func TestOrphanLedgerIsWrittenOnceAndKeepsOtherCodebasesEntries(t *testing.T) {
	base := t.TempDir()
	mine := filepath.Join(base, "mine")
	theirs := filepath.Join(base, "theirs")
	newRepo(t, mine)
	newRepo(t, theirs)

	configDir := t.TempDir()
	writeLegacyFact(t, configDir, mine, Memory{Name: "mine", Description: "d", Type: TypeProject, Body: "b"})
	writeLegacyFact(t, configDir, theirs, Memory{Name: "theirs", Description: "d", Type: TypeProject, Body: "b"})
	writeLegacyFact(t, configDir, filepath.Join(base, "nameless"), Memory{Name: "orphan", Description: "d", Type: TypeProject, Body: "b"})
	writeSession(t, configDir, mine, time.Now())
	writeSession(t, configDir, theirs, time.Now())

	a := New(configDir, mine)
	if err := a.Migrate(); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(a.orphanLedgerPath())
	if err != nil {
		t.Fatal(err)
	}

	// A second codebase migrating sees the same orphan and must not rewrite
	// the list to say the same thing — that rewrite is what a repeated warning
	// would be made of.
	b := New(configDir, theirs)
	if err := b.Migrate(); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(b.orphanLedgerPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("an unchanged orphan set should not rewrite the list:\n%s\n%s", first, second)
	}
	// And what one codebase copied must never be listed as an orphan just
	// because another one has no opinion about it.
	for _, e := range b.readOrphanLedger().Stores {
		if e.Facts != 1 || e.LastKnownCWD != "" {
			t.Errorf("unexpected entry %+v", e)
		}
		if e.Hash == filepath.Base(a.legacyProjectRoot) || e.Hash == filepath.Base(b.legacyProjectRoot) {
			t.Errorf("a migrated store was filed as an orphan: %+v", e)
		}
	}
}

func TestMigrateDoesNotSealOnACorruptSession(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)
	wt := filepath.Join(base, "wt")
	gitOrSkip(t, repo)("worktree", "add", "-b", "one", wt)

	configDir := t.TempDir()
	writeLegacyFact(t, configDir, wt, Memory{Name: "from-worktree", Description: "d", Type: TypeProject, Body: "b"})
	// The only record of where that store came from, truncated mid-write. The
	// facts are fine; the evidence is not, and the migration must say so
	// instead of publishing a store it knows is short.
	writeSession(t, configDir, wt, time.Now())
	corruptSessions(t, configDir)

	s := New(configDir, repo)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/from-worktree"); ok {
		t.Error("an unreadable session must not be guessed at")
	}
	rec, _ := s.readMigrationRecord()
	if rec.Complete {
		t.Error("a migration blinded by a corrupt session must not seal itself")
	}

	// The store is intact, so once the session is readable again the retry
	// picks it up: nothing about the failure is permanent.
	writeSession(t, configDir, wt, time.Now())
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/from-worktree"); !ok {
		t.Error("the retry should have found the store")
	}
}

func TestMigrateReadingACorruptSessionStaysCheap(t *testing.T) {
	configDir := t.TempDir()
	dir := filepath.Join(configDir, "sessions", "scope_x")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A session whose header cannot be decoded used to be read in full to try
	// again — for a damaged transcript that is megabytes off disk to answer a
	// question about the first hundred bytes.
	big := append([]byte(`{"metadata": [`), bytes.Repeat([]byte("x"), 4<<20)...)
	if err := os.WriteFile(filepath.Join(dir, "0123456789abcdef01234567.json"), big, 0o600); err != nil {
		t.Fatal(err)
	}

	scan, _ := session.ScanCWDs(filepath.Join(configDir, "sessions"))
	// Counted as unreadable rather than merely cwd-less: the difference
	// decides whether the migration retries, and this one is worth retrying.
	if scan.Unreadable != 1 || scan.NoCWD != 0 || scan.Unmappable() != 1 {
		t.Errorf("a session that cannot be decoded should be counted as unreadable, got %+v", scan)
	}
	if len(scan.CWDs) != 0 {
		t.Errorf("nothing should have been decoded, got %v", scan.CWDs)
	}
}

func TestMigrateWrapsASiblingWorktreesV1File(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)
	wt := filepath.Join(base, "wt")
	gitOrSkip(t, repo)("worktree", "add", "-b", "one", wt)

	configDir := t.TempDir()
	// A v1 MEMORY.md left in a worktree: MigrateV1IfNeeded only ever looks at
	// the directory being opened, so without the codebase migration carrying
	// it these notes would be invisible for good.
	wtRoot := New(configDir, wt).legacyProjectRoot
	if err := os.MkdirAll(wtRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtRoot, "MEMORY.md"), []byte("notes from the worktree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSession(t, configDir, wt, time.Now())

	s := New(configDir, repo)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	m, ok, _ := s.Read("project/" + v1FactName)
	if !ok {
		t.Fatal("a sibling worktree's v1 notes belong to the same codebase")
	}
	if !strings.Contains(m.Body, "notes from the worktree") {
		t.Errorf("the notes were not carried over: %q", m.Body)
	}
	rec, _ := s.readMigrationRecord()
	if !rec.Complete {
		t.Error("everything was accounted for; the migration should be complete")
	}
}

func TestMigrateDoesNotDuplicateOwnV1Notes(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	s := New(configDir, repo)
	if err := os.MkdirAll(s.legacyProjectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	// Both migrations can see this file now. Wrapping it twice would leave the
	// same notes under two IDs and make the index pay for them twice.
	if err := os.WriteFile(filepath.Join(s.legacyProjectRoot, "MEMORY.md"), []byte("flat notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range s.List() {
		if strings.HasPrefix(m.Name, v1FactName) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the v1 notes were wrapped %d times", n)
	}
	if fileExists(filepath.Join(s.legacyProjectRoot, "MEMORY.md")) {
		t.Error("the flat file should still be retired")
	}
}

func TestMigrateDoesNotFollowSymlinkedFacts(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	writeLegacyFact(t, configDir, repo, Memory{Name: "real", Description: "d", Type: TypeProject, Body: "b"})
	outside := filepath.Join(base, "outside.md")
	if err := os.WriteFile(outside, serialize(Memory{Name: "planted", Description: "d", Type: TypeProject, Body: "b"}), 0o600); err != nil {
		t.Fatal(err)
	}
	// A memory directory is user data other programs can write into. A link
	// planted there would have this migration republish a file from anywhere
	// on the filesystem as something the user asked moa to remember.
	if err := os.Symlink(outside, filepath.Join(legacyMemoryDir(t, configDir, repo), "planted.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	s := New(configDir, repo)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Read("project/planted"); ok {
		t.Error("a symlinked fact must not be migrated")
	}
	if _, ok, _ := s.Read("project/real"); !ok {
		t.Error("the real facts should still be there")
	}
}

func TestMigrateConcurrentProcessesCopyEachFactOnce(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)
	git := gitOrSkip(t, repo)
	wt := filepath.Join(base, "wt")
	git("worktree", "add", "-b", "one", wt)

	configDir := t.TempDir()
	writeLegacyFact(t, configDir, repo, Memory{Name: "from-main", Description: "d", Type: TypeProject, Body: "b"})
	writeLegacyFact(t, configDir, wt, Memory{Name: "from-wt", Description: "d", Type: TypeProject, Body: "b"})
	writeSession(t, configDir, wt, time.Now())

	// Two sessions starting at once in two worktrees of one repository: the
	// ordinary case, not a corner one. Without a lock both would copy, and the
	// loser used to conclude from the destination's existence that the winner
	// had done its work.
	//
	// The outcome alone does not prove the lock is doing anything — the merge
	// is additive and idempotent, so unlocked runs usually produce the same
	// two facts, and this test passed unchanged with the lock removed. What
	// only holds with a lock is that the critical sections do not overlap, so
	// that is what is asserted, through the one seam inside it. The hook
	// blocks long enough that an unlocked second entrant is certain to be
	// counted; with the lock, the second cannot enter until the first left.
	var mu sync.Mutex
	inside, maxInside := 0, 0
	migrationInsideLock = func() func() {
		mu.Lock()
		inside++
		if inside > maxInside {
			maxInside = inside
		}
		mu.Unlock()
		// Sleeping on the way in keeps the section occupied long enough that
		// an unlocked second entrant is certain to be counted, and the exit
		// runs after the merge, so releasing the lock early anywhere in
		// between is what this catches.
		time.Sleep(50 * time.Millisecond)
		return func() {
			mu.Lock()
			inside--
			mu.Unlock()
		}
	}
	t.Cleanup(func() { migrationInsideLock = nil })

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, cwd := range []string{repo, wt} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = New(configDir, cwd).Migrate()
		}()
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	mu.Lock()
	overlap := maxInside
	mu.Unlock()
	if overlap != 1 {
		t.Errorf("%d migrations were inside the critical section at once; the lock is not holding", overlap)
	}

	got := New(configDir, repo).List()
	if len(got) != 2 {
		t.Fatalf("each fact should be copied exactly once, got %+v", got)
	}
}

func TestMigrateLoserOfTheLockRereadsTheMarker(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newRepo(t, repo)

	configDir := t.TempDir()
	writeLegacyFact(t, configDir, repo, Memory{Name: "learned", Description: "d", Type: TypeProject, Body: "b"})

	// What the process that loses the race sees: it was let past the cheap
	// gate because no marker existed, and by the time it holds the lock the
	// winner has copied everything and sealed. It must believe the marker it
	// finds inside the lock rather than the state it observed outside, or it
	// copies a second time — which after a delete is how a fact the user threw
	// away comes back.
	//
	// The winner's work is replayed through the same unlocked steps a real
	// migration takes rather than by calling Migrate: two flocks on one file
	// block each other inside a single process too, so a nested Migrate would
	// deadlock instead of testing anything.
	winner := New(configDir, repo)
	ownHash := filepath.Base(winner.legacyProjectRoot)
	var once sync.Once
	migrationInsideLock = func() func() {
		once.Do(func() {
			legacy := scanLegacyStores(filepath.Join(configDir, "projects"))
			files, err := collectFacts([]factSource{{store: legacy[ownHash], cwd: repo, facts: 1}})
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := winner.mergeIntoDestination(files); err != nil {
				t.Error(err)
				return
			}
			if err := winner.writeMigrationRecord(migrationRecord{
				Complete: true,
				Copied:   []storeEntry{{Hash: ownHash, CWD: repo, Facts: 1}},
			}); err != nil {
				t.Error(err)
				return
			}
			// The user throws the fact away straight after: this is what a
			// second copy would undo.
			if err := winner.Delete("project/learned"); err != nil {
				t.Error(err)
			}
		})
		return func() {}
	}
	t.Cleanup(func() { migrationInsideLock = nil })

	loser := New(configDir, repo)
	if err := loser.Migrate(); err != nil {
		t.Fatal(err)
	}
	if got := loser.List(); len(got) != 0 {
		t.Errorf("the loser copied facts the winner had already accounted for: %+v", got)
	}
}

func TestRenameFactHeader(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"rewrites the name line only",
			"---\nname: foo\ndescription: d\ntype: project\nx-custom: kept\n---\n\nbody\n",
			"---\nname: foo-2\ndescription: d\ntype: project\nx-custom: kept\n---\n\nbody\n",
		},
		{
			"crlf survives",
			"---\r\nname: foo\r\ndescription: d\r\n---\r\n\r\nbody\r\n",
			"---\r\nname: foo-2\r\ndescription: d\r\n---\r\n\r\nbody\r\n",
		},
		{
			"no frontmatter is left alone",
			"just text\n",
			"just text\n",
		},
		{
			"no name key is left alone",
			"---\ndescription: d\n---\n\nbody\n",
			"---\ndescription: d\n---\n\nbody\n",
		},
		{
			"unterminated frontmatter is left alone",
			"---\nname: foo\ndescription: d\n",
			"---\nname: foo\ndescription: d\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(renameFactHeader([]byte(c.in), "foo-2")); got != c.want {
				t.Errorf("renameFactHeader() = %q, want %q", got, c.want)
			}
		})
	}
}
