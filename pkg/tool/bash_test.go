//go:build !windows

package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

// newStateCfg builds a persistent-shell ToolConfig rooted at a fresh temp dir
// with an unrestricted path policy. The workspace path is canonicalized so it
// matches the physical path `pwd` reports (macOS /var → /private/var).
func newStateCfg(t *testing.T) (ToolConfig, *BashState, string) {
	t.Helper()
	ws := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(ws); err == nil {
		ws = resolved
	}
	st := NewBashState()
	cfg := ToolConfig{
		WorkspaceRoot: ws,
		PathPolicy:    NewPathPolicy(ws, nil, true),
		BashState:     st,
	}
	return cfg, st, ws
}

// runBash executes the bash tool once and returns the result text.
func runBash(t *testing.T, ctx context.Context, cfg ToolConfig, command string, params map[string]any) string {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	params["command"] = command
	res, err := NewBash(cfg).Execute(ctx, params, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].Text
}

func TestBash_PersistsCwd(t *testing.T) {
	cfg, _, _ := newStateCfg(t)
	ctx := context.Background()

	runBash(t, ctx, cfg, "mkdir -p sub && cd sub", nil)
	out := runBash(t, ctx, cfg, "pwd", nil)
	if !strings.HasSuffix(strings.TrimSpace(out), "/sub") {
		t.Fatalf("expected pwd to end in /sub, got %q", out)
	}
}

// TestBash_DoesNotPersistStartupVars pins that BASH_ENV and exported functions
// never survive across calls: a persisted BASH_ENV would run its script on
// every later `bash -c` before the trap installs, and an exported function
// could shadow the trap's own commands. A plain export in the same call must
// still persist, proving the filter is selective, not a capture failure.
func TestBash_DoesNotPersistStartupVars(t *testing.T) {
	cfg, _, _ := newStateCfg(t)
	ctx := context.Background()

	runBash(t, ctx, cfg, `export BASH_ENV=/tmp/should-not-run; export KEEPME=yes; myfn() { echo hi; }; export -f myfn`, nil)

	if out := runBash(t, ctx, cfg, `echo "KEEPME=${KEEPME:-unset}"`, nil); !strings.Contains(out, "KEEPME=yes") {
		t.Fatalf("plain export did not persist (capture failure?): %q", out)
	}
	if out := runBash(t, ctx, cfg, `echo "BASH_ENV=${BASH_ENV:-unset}"`, nil); !strings.Contains(out, "BASH_ENV=unset") {
		t.Fatalf("BASH_ENV persisted across calls: %q", out)
	}
	if out := runBash(t, ctx, cfg, `type myfn >/dev/null 2>&1 && echo "myfn=present" || echo "myfn=absent"`, nil); !strings.Contains(out, "myfn=absent") {
		t.Fatalf("exported function persisted across calls: %q", out)
	}
}

func TestBash_PersistsEnv(t *testing.T) {
	cfg, st, _ := newStateCfg(t)
	ctx := context.Background()

	runBash(t, ctx, cfg, "export FOO=bar", nil)
	if out := runBash(t, ctx, cfg, `echo "$FOO"`, nil); strings.TrimSpace(out) != "bar" {
		t.Fatalf("expected FOO=bar to persist, got %q", out)
	}

	// Re-export replaces the value (cmd.Env substitutes, does not accumulate).
	runBash(t, ctx, cfg, "export FOO=baz", nil)
	if out := runBash(t, ctx, cfg, `echo "$FOO"`, nil); strings.TrimSpace(out) != "baz" {
		t.Fatalf("expected FOO=baz after re-export, got %q", out)
	}

	// Denylisted vars must never leak into the persisted env (test 13).
	_, env := st.Snapshot("")
	for _, e := range env {
		key := e
		if i := strings.IndexByte(e, '='); i >= 0 {
			key = e[:i]
		}
		if envDenylist[key] {
			t.Fatalf("denylisted var %q leaked into persisted env", key)
		}
	}
}

func TestBash_VenvStylePath(t *testing.T) {
	cfg, _, _ := newStateCfg(t)
	ctx := context.Background()

	// Mimic `activate`: put a dir with a stub binary on PATH via export.
	runBash(t, ctx, cfg,
		`mkdir -p bin && printf '#!/bin/sh\necho STUB_OK\n' > bin/mytool && chmod +x bin/mytool && export PATH="$PWD/bin:$PATH"`,
		nil)
	if out := runBash(t, ctx, cfg, "mytool", nil); !strings.Contains(out, "STUB_OK") {
		t.Fatalf("expected stub binary on persisted PATH to run, got %q", out)
	}
}

func TestBash_ExplicitCwdParamOverrides(t *testing.T) {
	cfg, st, ws := newStateCfg(t)
	ctx := context.Background()

	if err := os.MkdirAll(filepath.Join(ws, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirB := filepath.Join(ws, "b")
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatal(err)
	}

	// Persist cwd = a.
	runBash(t, ctx, cfg, "cd a", nil)

	// Explicit cwd=b must win over the persisted cwd, and update state to b.
	out := runBash(t, ctx, cfg, "pwd", map[string]any{"cwd": dirB})
	if !strings.HasSuffix(strings.TrimSpace(out), "/b") {
		t.Fatalf("expected pwd in /b, got %q", out)
	}
	if cwd, _ := st.Snapshot(""); !strings.HasSuffix(cwd, "/b") {
		t.Fatalf("expected persisted cwd to update to /b, got %q", cwd)
	}
}

func TestBash_ExitCodePreserved(t *testing.T) {
	cfg, st, _ := newStateCfg(t)
	ctx := context.Background()

	runBash(t, ctx, cfg, "mkdir -p sub && cd sub", nil)
	// `exit 3` still lets the EXIT trap run: exit code shown AND state updated.
	out := runBash(t, ctx, cfg, "exit 3", nil)
	if !strings.Contains(out, "Exit code: 3") {
		t.Fatalf("expected 'Exit code: 3', got %q", out)
	}
	if cwd, env := st.Snapshot(""); !strings.HasSuffix(cwd, "/sub") || env == nil {
		t.Fatalf("expected state captured through exit 3, got cwd=%q env=%v", cwd, env)
	}
}

func TestBash_TimeoutKeepsPriorState(t *testing.T) {
	cfg, st, _ := newStateCfg(t)
	ctx := context.Background()

	runBash(t, ctx, cfg, "mkdir -p sub && cd sub", nil)
	priorCwd, _ := st.Snapshot("")

	// A timed-out command dies by signal → trap doesn't run → state unchanged.
	out := runBash(t, ctx, cfg, "sleep 10", map[string]any{"timeout": 1})
	if !strings.Contains(out, "timed out") {
		t.Fatalf("expected timeout, got %q", out)
	}
	if cwd, _ := st.Snapshot(""); cwd != priorCwd {
		t.Fatalf("timeout corrupted state: cwd=%q, want %q", cwd, priorCwd)
	}
}

func TestBash_NilStateIsStateless(t *testing.T) {
	ws := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(ws); err == nil {
		ws = resolved
	}
	cfg := ToolConfig{WorkspaceRoot: ws, PathPolicy: NewPathPolicy(ws, nil, true)} // BashState nil
	ctx := context.Background()

	runBash(t, ctx, cfg, "mkdir -p sub && cd sub", nil)
	// Without persistence, the second call starts back at the workspace root.
	out := runBash(t, ctx, cfg, "pwd", nil)
	if strings.HasSuffix(strings.TrimSpace(out), "/sub") {
		t.Fatalf("nil state should not persist cwd, got %q", out)
	}
	if strings.TrimSpace(out) != ws {
		t.Fatalf("expected pwd = workspace root %q, got %q", ws, out)
	}

	// No exported var carries over either.
	runBash(t, ctx, cfg, "export FOO=bar", nil)
	if out := runBash(t, ctx, cfg, `echo "[$FOO]"`, nil); strings.TrimSpace(out) != "[]" {
		t.Fatalf("nil state should not persist env, got %q", out)
	}
}

func TestBash_StatePersistedCwdDeletedFallsBack(t *testing.T) {
	cfg, st, ws := newStateCfg(t)
	ctx := context.Background()

	// Persist a cwd, then delete the directory out from under the shell.
	runBash(t, ctx, cfg, "mkdir -p gone && cd gone", nil)
	cwd, _ := st.Snapshot("")
	if err := os.RemoveAll(cwd); err != nil {
		t.Fatal(err)
	}

	// The next call must silently fall back to the workspace root (no error).
	out := runBash(t, ctx, cfg, "pwd", nil)
	if strings.TrimSpace(out) != ws {
		t.Fatalf("expected fallback to workspace root %q, got %q", ws, out)
	}
}

func TestBash_EnvCaptureCapped(t *testing.T) {
	cfg, st, _ := newStateCfg(t)
	ctx := context.Background()

	// Seed a known env, then export a var larger than the capture cap.
	runBash(t, ctx, cfg, "export SMALL=ok", nil)
	_, priorEnv := st.Snapshot("")

	runBash(t, ctx, cfg, `export BIG=$(head -c 300000 /dev/zero | tr '\0' x)`, nil)

	// Env exceeded the cap → prior snapshot kept; BIG never persisted.
	_, env := st.Snapshot("")
	if len(env) != len(priorEnv) {
		t.Fatalf("expected env unchanged after oversize export (%d entries), got %d", len(priorEnv), len(env))
	}
	for _, e := range env {
		if strings.HasPrefix(e, "BIG=") {
			t.Fatal("oversize BIG var must not be persisted")
		}
	}
}

func TestBash_SubagentIsolation(t *testing.T) {
	cfg, st, ws := newStateCfg(t)
	other := filepath.Join(ws, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}

	rootCtx := core.WithAgentID(context.Background(), "root")
	childCtx := core.WithAgentID(context.Background(), "child")

	// Root moves into sub.
	runBash(t, rootCtx, cfg, "mkdir -p sub && cd sub", nil)

	// Seed the child from root: it inherits root's cwd (sub).
	st.Seed("child", "root")
	if out := runBash(t, childCtx, cfg, "pwd", nil); !strings.HasSuffix(strings.TrimSpace(out), "/sub") {
		t.Fatalf("child should inherit root cwd /sub, got %q", out)
	}

	// The child moves elsewhere; root's snapshot must stay at sub.
	runBash(t, childCtx, cfg, "cd "+other, nil)
	if cwd, _ := st.Snapshot("root"); !strings.HasSuffix(cwd, "/sub") {
		t.Fatalf("child cd leaked into root: root cwd=%q, want .../sub", cwd)
	}
	if cwd, _ := st.Snapshot("child"); !strings.HasSuffix(cwd, "/other") {
		t.Fatalf("child cwd = %q, want .../other", cwd)
	}

	// A child that was never seeded starts at the workspace root.
	fresh := core.WithAgentID(context.Background(), "unseeded")
	if out := runBash(t, fresh, cfg, "pwd", nil); strings.TrimSpace(out) != ws {
		t.Fatalf("unseeded child should start at workspace root %q, got %q", ws, out)
	}
}

func TestBash_UserTrapDoesNotCorruptState(t *testing.T) {
	cfg, st, _ := newStateCfg(t)
	ctx := context.Background()

	runBash(t, ctx, cfg, "export MARKER=xyz && cd sub 2>/dev/null; mkdir -p sub && cd sub", nil)
	priorCwd, priorEnv := st.Snapshot("")

	// A user EXIT trap overrides ours → our capture files are never written →
	// state is left untouched (no partial/corrupt update).
	runBash(t, ctx, cfg, "export MARKER=changed; trap ':' EXIT", nil)

	cwd, env := st.Snapshot("")
	if cwd != priorCwd {
		t.Fatalf("user trap corrupted cwd: %q, want %q", cwd, priorCwd)
	}
	var got string
	for _, e := range env {
		if strings.HasPrefix(e, "MARKER=") {
			got = e
		}
	}
	var want string
	for _, e := range priorEnv {
		if strings.HasPrefix(e, "MARKER=") {
			want = e
		}
	}
	if got != want || want != "MARKER=xyz" {
		t.Fatalf("user trap corrupted env: MARKER=%q, want MARKER=xyz", got)
	}
}
