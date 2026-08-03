package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitCommonDirTimeout caps the git call CodebaseKey makes. It runs on session
// startup, so a git hanging on an unresponsive network mount would hang the
// startup with it; past the deadline the key falls back to the path.
//
// pkg/git has its own cmdTimeout with the same value. The duplication is
// deliberate: pkg/core is imported by pkg/git's callers, and importing pkg/git
// from here to share a two-line constant would buy a dependency far heavier
// than the literal it saves.
const gitCommonDirTimeout = 2 * time.Second

// CodebaseKey identifies the repository a directory belongs to, so that every
// git worktree of the same repo answers with the same key.
//
// ProjectHash cannot do this: it hashes the canonical path, and a worktree is
// by definition a different path. Anything scoped by ProjectHash is therefore
// scoped per worktree, and deleting a worktree orphans whatever was stored
// under it — with a dozen worktrees on one repo, that is a dozen unrelated
// islands and a permanent leak every time one is removed.
//
// A directory inside a submodule keys on the submodule, not on the
// superproject: a submodule is a repository with its own history and its own
// remote, usually maintained by other people, and what is learned about it
// belongs to it rather than to whoever vendored it. The corollary is that the
// same submodule checked out under two worktrees of the superproject gets two
// keys, because git gives each one its own git dir (super/.git/modules/... vs
// super/.git/worktrees/<wt>/...) and nothing ties them back together without
// resolving the superproject, which would defeat the decision above.
//
// Outside a repository, or when git cannot answer, the key is the hash of the
// canonical path: the current behaviour. It never fails and never returns "".
func CodebaseKey(dir string) string {
	if root := gitCodebaseRoot(dir); root != "" {
		return hashKey(root)
	}
	canonical, err := CanonicalizePath(dir)
	if err != nil {
		canonical = filepath.Clean(dir)
	}
	return hashKey(canonical)
}

// RepoCodebaseKey is CodebaseKey restricted to directories git recognizes as
// part of a repository: it reports no key at all instead of falling back to
// the path hash.
//
// The distinction matters to anything that uses a directory as *evidence*
// about who owns something rather than as the workspace it was asked about.
// The fallback is the right answer for "which key does this workspace use" —
// it always answers, and unrelated paths get unrelated keys — but as evidence
// it is worthless: it would name an owner for every path on the filesystem,
// including ones no workspace will ever open.
func RepoCodebaseKey(dir string) (string, bool) {
	root := gitCodebaseRoot(dir)
	if root == "" {
		return "", false
	}
	return hashKey(root), true
}

// hashKey mirrors ProjectHash's output shape (16 hex chars) so directories
// named after either function look alike on disk.
func hashKey(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// gitCodebaseRoot returns a canonical path shared by every worktree of the
// repository containing dir, or "" if dir is not in a repository.
//
// It shells out to `git rev-parse` rather than walking the filesystem for a
// .git entry. Walking means reimplementing git: parsing the `gitdir:` pointer
// file a worktree uses, climbing out of `worktrees/<name>`, and still getting
// submodules and moved worktrees wrong. git already knows the answer, and one
// exec per session is not a cost worth a private reimplementation of a format
// git may extend.
//
// Both values it needs come from that single exec: whether the repository is
// bare decides whether the ".git" suffix may be stripped, and asking for it
// separately would double the cost of the only git call on the startup path.
func gitCodebaseRoot(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommonDirTimeout)
	defer cancel()
	// --is-bare-repository comes first because it is a fixed token: whatever
	// follows the first newline is the common dir, even when the path itself
	// contains newlines.
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--is-bare-repository", "--git-common-dir")
	cmd.Dir = dir
	cmd.Env = gitEnv()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		// Not a repository, git missing from PATH, dir gone, or timed out.
		return ""
	}
	bare, common, ok := strings.Cut(stdout.String(), "\n")
	if !ok {
		return ""
	}
	// --is-bare-repository is contractually true or false. Anything else means
	// we are not reading the output we think we are, and the answer decides
	// whether the ".git" suffix gets stripped below — so fail closed to the
	// path identity rather than guess a layout from a value we cannot parse.
	// Cut already removed the "\n"; a "\r" can still be in front of it.
	bare = strings.TrimSuffix(bare, "\r")
	if bare != "true" && bare != "false" {
		return ""
	}
	common = trimGitEOL(common)
	if common == "" {
		return ""
	}
	// git answers relative to the process working directory when it can (".",
	// "../../.git"), so it only means anything joined back onto dir.
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	canonical, err := CanonicalizePath(common)
	if err != nil {
		canonical = filepath.Clean(common)
	}
	// Drop the trailing ".git": in the ordinary layout the common dir is
	// <repo>/.git, so stripping it makes the key the hash of the repository
	// path itself — the same value ProjectHash already produces for a plain
	// checkout, which keeps single-worktree projects on the identity they have
	// today. A bare layout (moa's own .bare, or a repo.git mirror) has no such
	// suffix and is left alone; its worktrees all report that same bare dir
	// anyway.
	//
	// The suffix alone does not prove the layout: `git init --bare x/.git` is
	// legal, and stripping there would hand the bare repository the identity of
	// the directory that merely holds it. So strip only once git says the
	// repository is not bare. git answers per worktree, so a linked worktree of
	// such a bare repo still reports "false" and still strips; that splits an
	// already pathological layout into two keys instead of merging two
	// codebases into one, which is the failure worth avoiding.
	if bare != "true" && filepath.Base(canonical) == ".git" {
		canonical = filepath.Dir(canonical)
	}
	return canonical
}

// trimGitEOL removes the line terminator git puts after each rev-parse value,
// and nothing else. strings.TrimSpace would also eat spaces that belong to the
// path: for a repository living under a directory named "bare " it would
// answer for "bare", so the repo and its worktrees would stop sharing a key —
// the one invariant this file exists to hold.
func trimGitEOL(s string) string {
	return strings.TrimSuffix(strings.TrimSuffix(s, "\n"), "\r")
}

// gitEnv is the inherited environment minus the variables that would let git
// answer about a repository other than the one containing cmd.Dir.
//
// exec.Cmd inherits the whole environment when Env is nil, and moa runs
// commands on the user's behalf: a stray GIT_DIR exported by a tool, a hook or
// the agent itself would silently give every directory the identity of that
// repository, merging the memory of two unrelated projects. Everything else is
// kept, so PATH, HOME and the user's git configuration keep working.
func gitEnv() []string {
	inherited := os.Environ()
	env := make([]string, 0, len(inherited))
	for _, kv := range inherited {
		name, _, _ := strings.Cut(kv, "=")
		if overridesGitIdentity(name) {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// overridesGitIdentity reports whether a variable can change which repository
// git reports for a given directory, or whether it reports it as bare.
func overridesGitIdentity(name string) bool {
	switch name {
	// Point git straight at another repository, work tree or object store.
	case "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE":
		return true
	// Change how far up the tree discovery goes, so the same directory can be
	// found to be in a repository or not depending on who exported what.
	case "GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM":
		return true
	// Config injected with the precedence of `git -c`, which can forge
	// core.bare (verified: it flips --is-bare-repository) and core.worktree.
	// GIT_CONFIG_GLOBAL/SYSTEM are deliberately not here: they select the
	// user's own config files, which must keep applying.
	case "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT":
		return true
	}
	// The numbered pairs GIT_CONFIG_COUNT reads.
	return strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_")
}
