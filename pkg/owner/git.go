package owner

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// The canonical ref is the branch the book's areas/ describe. Everything else
// is work in progress and lives in work/<feature>.md until it lands.
//
// It has to be a stored fact rather than something the owner infers: a report
// carries the branch the work happened on, and in a worktree ("design-visual",
// "feat/x") there is nothing in that string that says which of them is the
// default. Without it, "default branch → areas/, another branch → work/" is an
// instruction the model cannot follow deterministically.

// canonicalRefTimeout bounds the detection. It runs once, when the owner is
// created; a slow filesystem must not block the creation.
var canonicalRefTimeout = 3 * time.Second

// DetectCanonicalRef asks the repository which branch is canonical:
// origin/HEAD when the remote says so, otherwise master or main if one of them
// exists. A directory that is not a repository (or a git that does not answer)
// gives "", which every reader renders as an explicit unknown rather than
// guessing a name.
//
// It is stored in owner.json and meant to be edited by hand: a project whose
// truth lives on "develop" says so there.
func DetectCanonicalRef(root string) string {
	if root == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), canonicalRefTimeout)
	defer cancel()
	run := func(args ...string) (string, bool) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		// See pkg/serve/reports.go: the deadline alone does not bound a child
		// that holds the output pipe.
		cmd.WaitDelay = time.Second
		out, err := cmd.Output()
		if err != nil {
			return "", false
		}
		return strings.TrimSpace(string(out)), true
	}
	if _, ok := run("rev-parse", "--git-dir"); !ok {
		return "" // not a repository, or no git: unknown, and said so
	}
	if ref, ok := run("symbolic-ref", "refs/remotes/origin/HEAD"); ok && ref != "" {
		if name := strings.TrimPrefix(ref, "refs/remotes/origin/"); name != ref && name != "" {
			return name
		}
	}
	for _, candidate := range []string{"master", "main"} {
		if _, ok := run("show-ref", "--verify", "--quiet", "refs/heads/"+candidate); ok {
			return candidate
		}
	}
	return ""
}
