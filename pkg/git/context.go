// Package git provides lightweight git metadata for the agent's system prompt.
package git

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// cmdTimeout caps git commands to prevent hangs on network mounts.
const cmdTimeout = 2 * time.Second

// Context returns a human-readable summary of the git state for cwd.
// Returns "" if cwd is not in a git repo or git is not available.
// Never returns an error — failures are silent.
//
// Uncommitted porcelain is intentionally omitted: it changes as the agent
// works, and GPT-5.6 implicit prompt cache misses the entire prefix when
// any earlier instructions byte changes. Branch and last commit are enough
// to orient the model; it can run git status for the dirty tree.
func Context(cwd string) string {
	if !isRepo(cwd) {
		return ""
	}

	var parts []string

	if branch := run(cwd, "rev-parse", "--abbrev-ref", "HEAD"); branch != "" {
		parts = append(parts, "Branch: "+branch)
	}

	if lastCommit := run(cwd, "log", "--oneline", "-1"); lastCommit != "" {
		parts = append(parts, "Last commit: "+lastCommit)
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}

func isRepo(cwd string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = cwd
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func run(cwd string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(stdout.String())
}
