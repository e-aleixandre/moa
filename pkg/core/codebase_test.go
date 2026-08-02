package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// gitRunner returns a helper that runs git in dir, skipping the test when git
// is not installed rather than failing on a machine that never had it.
func gitRunner(t *testing.T, dir string) func(args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	return func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// Build fixtures in the same sanitized environment the code under test
		// uses. Inheriting a hostile GIT_DIR would point these commands at
		// another repository and the suite would fail while setting up, hiding
		// whatever CodebaseKey actually does with it.
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// newTestRepo creates a repository with one commit, so `git worktree add` has
// something to branch from.
func newTestRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := gitRunner(t, dir)
	git("init")
	git("config", "user.email", "t@t.t")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "init")
}

func TestCodebaseKey_WorktreesShareTheKey(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newTestRepo(t, repo)
	git := gitRunner(t, repo)

	wt1 := filepath.Join(base, "wt1")
	wt2 := filepath.Join(base, "wt2")
	git("worktree", "add", "-b", "one", wt1)
	git("worktree", "add", "-b", "two", wt2)

	// The whole point: worktrees are different paths, so ProjectHash gives
	// them different identities and deleting one orphans its state.
	if ProjectHash(wt1) == ProjectHash(wt2) {
		t.Fatal("precondition: distinct paths must hash differently")
	}
	if got1, got2 := CodebaseKey(wt1), CodebaseKey(wt2); got1 != got2 {
		t.Fatalf("two worktrees of one repo must share a key: %s != %s", got1, got2)
	}
	if got := CodebaseKey(repo); got != CodebaseKey(wt1) {
		t.Fatalf("main checkout and its worktree must share a key: %s != %s", got, CodebaseKey(wt1))
	}
}

func TestCodebaseKey_MainCheckoutKeepsPathIdentity(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	newTestRepo(t, repo)

	// Stripping the ".git" suffix off the common dir is what keeps an ordinary
	// single-checkout project on the identity it already has, so nothing
	// scoped by this key has to be migrated for the common case.
	if got, want := CodebaseKey(repo), ProjectHash(repo); got != want {
		t.Fatalf("plain checkout should key on the repo path: got %s, want %s", got, want)
	}
}

func TestCodebaseKey_DistinctReposDiffer(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	newTestRepo(t, a)
	newTestRepo(t, b)

	if CodebaseKey(a) == CodebaseKey(b) {
		t.Fatal("unrelated repositories must not collide")
	}
}

func TestCodebaseKey_BareLayoutWorktrees(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	newTestRepo(t, src)

	// moa's own repo uses this layout: a bare .bare directory with every branch
	// checked out as a worktree beside it. There is no ".git" suffix to strip
	// here, and the key must still be stable across the worktrees.
	bare := filepath.Join(base, ".bare")
	gitRunner(t, base)("clone", "--bare", src, bare)
	git := gitRunner(t, bare)
	wt1 := filepath.Join(base, "wt1")
	wt2 := filepath.Join(base, "wt2")
	git("worktree", "add", "-b", "one", wt1)
	git("worktree", "add", "-b", "two", wt2)

	if got1, got2 := CodebaseKey(wt1), CodebaseKey(wt2); got1 != got2 {
		t.Fatalf("bare-layout worktrees must share a key: %s != %s", got1, got2)
	}
	if CodebaseKey(wt1) == CodebaseKey(src) {
		t.Fatal("a bare clone is a different codebase from its source checkout")
	}
}

func TestCodebaseKey_SubdirectoryMatchesRoot(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	newTestRepo(t, repo)
	deep := filepath.Join(repo, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	// From a subdirectory git answers with a relative path ("../../../.git"),
	// which is meaningless unless it is joined back onto the directory asked
	// about.
	if got, want := CodebaseKey(deep), CodebaseKey(repo); got != want {
		t.Fatalf("a subdirectory belongs to the same codebase: got %s, want %s", got, want)
	}
}

func TestCodebaseKey_NoGitFallsBackToPath(t *testing.T) {
	dir := t.TempDir() // not a repository

	if got, want := CodebaseKey(dir), ProjectHash(dir); got != want {
		t.Fatalf("outside a repo the key must stay the path hash: got %s, want %s", got, want)
	}
	if got := CodebaseKey(dir); got == "" || len(got) != 16 {
		t.Fatalf("key must always be 16 hex chars, got %q", got)
	}
}

func TestCodebaseKey_MissingDirStillAnswers(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")

	// git cannot even start with a working directory that does not exist; the
	// caller still gets a usable key instead of "" or a panic.
	if got := CodebaseKey(missing); len(got) != 16 {
		t.Fatalf("key must always be 16 hex chars, got %q", got)
	}
}

func TestCodebaseKey_PathSpellingIsIrrelevant(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newTestRepo(t, repo)

	want := CodebaseKey(repo)
	sep := string(filepath.Separator)
	// Spellings built by hand, not with filepath.Join, which would clean them
	// before CodebaseKey ever sees them.
	for _, spelling := range []string{
		repo + sep,
		repo + sep + ".",
		repo + sep + "." + sep,
		filepath.Dir(repo) + sep + "." + sep + filepath.Base(repo),
	} {
		if got := CodebaseKey(spelling); got != want {
			t.Fatalf("CodebaseKey(%q) = %s, want %s", spelling, got, want)
		}
	}

	// And the same directory asked twice never drifts.
	if got := CodebaseKey(repo); got != want {
		t.Fatalf("CodebaseKey is not idempotent: %s != %s", got, want)
	}
}

func TestCodebaseKey_PathWithTrailingSpace(t *testing.T) {
	base := t.TempDir()
	// A directory whose name ends in a space is legal on the platforms moa
	// runs on, but not everywhere; skip rather than fail where the filesystem
	// mangles the name.
	bare := filepath.Join(base, "bare ")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Skipf("filesystem rejects a trailing space: %v", err)
	}
	if _, err := os.Stat(bare); err != nil {
		t.Skipf("filesystem does not keep the trailing space: %v", err)
	}
	src := filepath.Join(base, "src")
	newTestRepo(t, src)
	gitRunner(t, base)("clone", "--bare", src, bare)

	git := gitRunner(t, bare)
	wt := filepath.Join(base, "wt")
	git("worktree", "add", "-b", "one", wt)

	// git terminates the value with a newline, and only the newline may go:
	// trimming whitespace would turn "…/bare " into "…/bare", so the worktree
	// and the repository it belongs to would hash to different codebases.
	if got, want := CodebaseKey(wt), CodebaseKey(bare); got != want {
		t.Fatalf("a path ending in a space must not lose it: %s != %s", got, want)
	}
	if got, want := CodebaseKey(bare), ProjectHash(bare); got != want {
		t.Fatalf("bare repo key should hash its own path: got %s, want %s", got, want)
	}
	if CodebaseKey(wt) == ProjectHash(filepath.Join(base, "bare")) {
		t.Fatal("key was computed from the space-stripped path")
	}
}

func TestTrimGitEOL(t *testing.T) {
	cases := map[string]string{
		"/repo/.git\n":       "/repo/.git",
		"/repo/.git\r\n":     "/repo/.git",
		"/path/bare \n":      "/path/bare ",
		"/path/bare \r\n":    "/path/bare ",
		"  /leading/space\n": "  /leading/space",
		"\n":                 "",
		"plain":              "plain",
	}
	for in, want := range cases {
		if got := trimGitEOL(in); got != want {
			t.Errorf("trimGitEOL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCodebaseKey_IgnoresInheritedGitEnv pins that the key describes the
// directory asked about and nothing else. moa runs commands on the agent's
// behalf, so any of these variables can end up exported in its own process,
// and honouring them would silently merge the memory of unrelated projects.
// Each case is built so that git would answer differently if the variable
// reached it.
func TestCodebaseKey_IgnoresInheritedGitEnv(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newTestRepo(t, repo)
	other := filepath.Join(base, "other")
	newTestRepo(t, other)

	// A bare repository whose common dir ends in ".git": the ".git" strip is
	// suppressed only because git reports it as bare, so anything that forges
	// that answer moves the key onto the parent directory.
	holder := filepath.Join(base, "holder")
	bare := filepath.Join(holder, ".git")
	if err := os.MkdirAll(holder, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRunner(t, base)("init", "--bare", bare)

	t.Run("GIT_DIR", func(t *testing.T) {
		// Points git at a repository from a directory that has none.
		outsider := filepath.Join(base, "outsider")
		if err := os.MkdirAll(outsider, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
		if got, want := CodebaseKey(outsider), ProjectHash(outsider); got != want {
			t.Fatalf("GIT_DIR moved the key: got %s, want %s", got, want)
		}
	})

	t.Run("GIT_COMMON_DIR", func(t *testing.T) {
		// Redirects the very value CodebaseKey reads, from inside a real repo.
		t.Setenv("GIT_COMMON_DIR", filepath.Join(other, ".git"))
		if got, want := CodebaseKey(repo), ProjectHash(repo); got != want {
			t.Fatalf("GIT_COMMON_DIR moved the key: got %s, want %s", got, want)
		}
	})

	t.Run("GIT_WORK_TREE", func(t *testing.T) {
		// Attaching a work tree makes git stop calling the repository bare.
		t.Setenv("GIT_WORK_TREE", holder)
		if got, want := CodebaseKey(bare), ProjectHash(bare); got != want {
			t.Fatalf("GIT_WORK_TREE moved the key: got %s, want %s", got, want)
		}
	})

	t.Run("GIT_CONFIG", func(t *testing.T) {
		// Injected config has the precedence of `git -c`, so it can forge
		// core.bare and with it the ".git" strip decision.
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "core.bare")
		t.Setenv("GIT_CONFIG_VALUE_0", "false")
		if got, want := CodebaseKey(bare), ProjectHash(bare); got != want {
			t.Fatalf("forged core.bare moved the key: got %s, want %s", got, want)
		}
	})

	t.Run("GIT_CEILING_DIRECTORIES", func(t *testing.T) {
		// A ceiling stops discovery below the repository root, so a
		// subdirectory would look like no repository at all.
		deep := filepath.Join(repo, "a")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		// git ignores ceiling entries that are not real paths.
		canonicalRepo, err := CanonicalizePath(repo)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_CEILING_DIRECTORIES", canonicalRepo)
		if got, want := CodebaseKey(deep), CodebaseKey(repo); got != want {
			t.Fatalf("a ceiling hid the repository: got %s, want %s", got, want)
		}
	})
}

func TestCodebaseKey_BareRepoNamedDotGit(t *testing.T) {
	base := t.TempDir()
	holder := filepath.Join(base, "holder")
	bare := filepath.Join(holder, ".git")
	if err := os.MkdirAll(holder, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRunner(t, base)("init", "--bare", bare)

	// `git init --bare <dir>/.git` is legal, and there the common dir really is
	// the repository. Stripping the suffix blindly would hand it the identity
	// of the directory that merely contains it.
	if got := CodebaseKey(bare); got == ProjectHash(holder) {
		t.Fatalf("a bare repository named .git must not key on its parent (%s)", got)
	}
	if got, want := CodebaseKey(bare), ProjectHash(bare); got != want {
		t.Fatalf("a bare repository keys on itself: got %s, want %s", got, want)
	}
}

func TestCodebaseKey_SubmoduleIsItsOwnCodebase(t *testing.T) {
	base := t.TempDir()
	child := filepath.Join(base, "child")
	super := filepath.Join(base, "super")
	newTestRepo(t, child)
	newTestRepo(t, super)

	git := gitRunner(t, super)
	// git refuses file:// submodules by default since CVE-2022-39253.
	git("-c", "protocol.file.allow=always", "submodule", "add", child, "vendor/child")
	git("-c", "protocol.file.allow=always", "commit", "-m", "add submodule")

	// A submodule is someone else's repository with its own history and
	// remote; what is learned inside it belongs to it, not to whoever vendored
	// it. Documented on CodebaseKey.
	sub := filepath.Join(super, "vendor", "child")
	if CodebaseKey(sub) == CodebaseKey(super) {
		t.Fatal("a submodule must not inherit the superproject's key")
	}
}

func TestCodebaseKey_RejectsUnparsableBareAnswer(t *testing.T) {
	// A stand-in git that succeeds but answers something other than
	// true/false. The layout decision below reads that token, so an
	// unrecognized value must fall back to the path identity instead of being
	// treated as "not bare" — which would strip a ".git" suffix and hand a
	// bare repository the identity of the directory holding it.
	if runtime.GOOS == "windows" {
		t.Skip("shell stub is POSIX")
	}
	bin := t.TempDir()
	// The stub reports a common dir one level *below* the working directory,
	// so the two outcomes are distinguishable: stripping ".git" would yield
	// <dir>/sub, while failing closed yields <dir> itself. Answering
	// "<dir>/.git" would strip back to <dir> and match the fallback by
	// coincidence, making the test pass either way.
	stub := "#!/bin/sh\nprintf 'not-a-bool\\n%s/sub/.git\\n' \"$PWD\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	// Prepend rather than replace: the stub is a shell script, so wiping PATH
	// would leave the kernel unable to run /bin/sh and the stub would fail to
	// execute — which also produces a fallback, and the test would pass for
	// the wrong reason.
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	if got, want := CodebaseKey(dir), ProjectHash(dir); got != want {
		t.Fatalf("unparsable bare answer must fall back to the path: got %s, want %s", got, want)
	}
}

func TestCodebaseKey_UnusedByProjectHash(t *testing.T) {
	// Permissions stay scoped per working directory on purpose: sharing memory
	// shares knowledge, sharing permissions shares authority. This guards the
	// split by pinning ProjectHash to the path.
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	newTestRepo(t, repo)
	git := gitRunner(t, repo)
	wt := filepath.Join(base, "wt")
	git("worktree", "add", "-b", "one", wt)

	if ProjectHash(repo) == ProjectHash(wt) {
		t.Fatal("ProjectHash must keep worktrees separate")
	}
}
