//go:build !windows

package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// denyAll rejects every directory, standing in for a session sandboxed to its
// workspace.
type denyAll struct{}

func (denyAll) IsAllowed(string) bool { return false }

// allowOnly permits one directory, the shape a PathPolicy takes after the user
// runs `/path add`.
type allowOnly struct{ dir string }

func (a allowOnly) IsAllowed(p string) bool { return p == a.dir }

func TestResolveWorkDir_EmptyKeepsSessionCWD(t *testing.T) {
	session := t.TempDir()
	// A deny-all policy must not matter: no override means no new access.
	got, err := ResolveWorkDir(session, "", denyAll{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != session {
		t.Fatalf("got %q, want session cwd %q", got, session)
	}
	if got, err := ResolveWorkDir(session, "   ", denyAll{}); err != nil || got != session {
		t.Fatalf("blank override: got %q, %v", got, err)
	}
}

func TestResolveWorkDir_RelativeResolvesAgainstSession(t *testing.T) {
	session := t.TempDir()
	sub := filepath.Join(session, "worktree")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveWorkDir(session, "worktree", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// EvalSymlinks resolves /var vs /private/var on macOS, so compare resolved.
	want, _ := filepath.EvalSymlinks(sub)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveWorkDir_AbsoluteOutsideSessionIsAllowedByPolicy(t *testing.T) {
	session := t.TempDir()
	other := t.TempDir()
	real, _ := filepath.EvalSymlinks(other)

	if _, err := ResolveWorkDir(session, other, allowOnly{real}); err != nil {
		t.Fatalf("allowed dir rejected: %v", err)
	}
	// The same directory, once the policy stops allowing it.
	_, err := ResolveWorkDir(session, other, denyAll{})
	if err == nil {
		t.Fatal("expected a disallowed directory to be rejected")
	}
	if !strings.Contains(err.Error(), "/path add") {
		t.Fatalf("error should say how to fix it, got: %v", err)
	}
}

func TestResolveWorkDir_NilPolicyAllowsAnything(t *testing.T) {
	session := t.TempDir()
	other := t.TempDir()
	if _, err := ResolveWorkDir(session, other, nil); err != nil {
		t.Fatalf("nil policy should not restrict: %v", err)
	}
}

// A typed nil *tool.PathPolicy stored in the PathChecker interface is not ==
// nil, so the guard in ResolveWorkDir would not catch it. The method itself has
// to tolerate a nil receiver, which is what this pins down.
func TestResolveWorkDir_TypedNilPolicyDoesNotPanic(t *testing.T) {
	session := t.TempDir()
	other := t.TempDir()
	var policy *nilSafePolicy
	var checker PathChecker = policy
	if _, err := ResolveWorkDir(session, other, checker); err != nil {
		t.Fatalf("typed-nil policy should allow: %v", err)
	}
}

type nilSafePolicy struct{}

func (p *nilSafePolicy) IsAllowed(string) bool { return p == nil }

func TestResolveWorkDir_RejectsMissingAndNonDirectories(t *testing.T) {
	session := t.TempDir()

	if _, err := ResolveWorkDir(session, "does-not-exist", nil); err == nil {
		t.Fatal("expected an error for a missing directory")
	}

	file := filepath.Join(session, "verify.json")
	if err := os.WriteFile(file, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveWorkDir(session, file, nil)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected a not-a-directory error, got: %v", err)
	}
}

// The multi-repo case this feature exists for: the session runs in one
// checkout, the checks live in another.
func TestTool_CWDRunsAnotherWorktreesChecks(t *testing.T) {
	session := t.TempDir()
	other := t.TempDir()
	writeVerifyJSON(t, session, Config{Checks: []Check{{Name: "session", Command: "exit 1"}}})
	writeVerifyJSON(t, other, Config{Checks: []Check{{Name: "other", Command: "echo ok"}}})

	tool := NewTool(session, nil)
	result, err := tool.Execute(context.Background(), map[string]any{"cwd": other}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(result)
	if result.IsError {
		t.Fatalf("expected the other worktree's passing check to pass, got: %s", text)
	}
	if !strings.Contains(text, "other") || strings.Contains(text, "session") {
		t.Fatalf("ran the wrong directory's checks: %s", text)
	}
	// The directory is named only when it is not the session's, so a
	// multi-repo run is never ambiguous in the transcript.
	if !strings.Contains(text, "in "+other) {
		t.Fatalf("preamble should name the target directory: %s", text)
	}
}

func TestTool_WithoutCWDKeepsSessionDirectory(t *testing.T) {
	session := t.TempDir()
	writeVerifyJSON(t, session, Config{Checks: []Check{{Name: "session", Command: "echo ok"}}})

	tool := NewTool(session, denyAll{})
	result, err := tool.Execute(context.Background(), map[string]any{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(result)
	if result.IsError {
		t.Fatalf("session checks should still run under a deny-all policy: %s", text)
	}
	// No directory is mentioned when it is the session's own.
	if strings.Contains(text, "in "+session) {
		t.Fatalf("preamble should stay terse for the session's own directory: %s", text)
	}
}

func TestTool_CWDOutsidePolicyIsRefused(t *testing.T) {
	session := t.TempDir()
	other := t.TempDir()
	writeVerifyJSON(t, other, Config{Checks: []Check{{Name: "other", Command: "echo pwned"}}})

	tool := NewTool(session, denyAll{})
	result, err := tool.Execute(context.Background(), map[string]any{"cwd": other}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("a directory outside the policy must not have its commands run")
	}
	if text := resultText(result); !strings.Contains(text, "/path add") {
		t.Fatalf("error should tell the user how to allow it, got: %s", text)
	}
}

// Models emit absent, null and empty optional fields interchangeably; none of
// them mean "another directory".
func TestTool_CWDIgnoresNonStringValues(t *testing.T) {
	session := t.TempDir()
	writeVerifyJSON(t, session, Config{Checks: []Check{{Name: "session", Command: "echo ok"}}})
	tool := NewTool(session, nil)

	for name, params := range map[string]map[string]any{
		"absent": {},
		"null":   {"cwd": nil},
		"empty":  {"cwd": ""},
		"number": {"cwd": 42},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := tool.Execute(context.Background(), params, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.IsError {
				t.Fatalf("should have fallen back to the session dir: %s", resultText(result))
			}
		})
	}
}

// Without a config the tool is still registered, so the message has to say
// where it looked and what to do about it.
func TestTool_NoConfigInTargetNamesTheDirectory(t *testing.T) {
	session := t.TempDir()
	other := t.TempDir()

	tool := NewTool(session, nil)
	result, err := tool.Execute(context.Background(), map[string]any{"cwd": other}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected an error result when the target has no config")
	}
	if text := resultText(result); !strings.Contains(text, other) {
		t.Fatalf("message should name the directory searched, got: %s", text)
	}
}

// A directory can be allowed while its config is not: .moa or verify.json may
// be symlinks pointing out of the sandbox. Running them would execute commands
// from a location the user never approved.
func TestResolveWorkDir_RefusesConfigSymlinkedOutside(t *testing.T) {
	session := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "verify.json"), []byte(`{"checks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(session, ".moa")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	realSession, _ := filepath.EvalSymlinks(session)

	_, err := ResolveWorkDir(session, session, allowOnly{realSession})
	if err == nil {
		t.Fatal("a config symlinked outside the sandbox must be refused")
	}
	if !strings.Contains(err.Error(), "resolves to") {
		t.Fatalf("error should show where it really points, got: %v", err)
	}

	// Unrestricted sessions asked for exactly this freedom.
	if _, err := ResolveWorkDir(session, session, nil); err != nil {
		t.Fatalf("nil policy should not restrict: %v", err)
	}
}
