package bus

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/goal"
	"github.com/e-aleixandre/moa/pkg/tool"
)

func gitInit(t *testing.T, dir string) {
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

func enter(t *testing.T, root string, allowed []string, cmd EnterGoal) *SessionContext {
	b := NewLocalBus()
	t.Cleanup(b.Close)
	runEnded := make(chan RunEnded, 1)
	b.Subscribe(func(e RunEnded) { runEnded <- e })
	sctx := &SessionContext{SessionID: t.Name(), SessionCtx: context.Background(), Bus: b,
		Agent: &fakeAgent{sendErr: errors.New("stop")}, State: NewStateMachine(b, t.Name()),
		Goal: goal.New(), CWD: root, PathPolicy: tool.NewPathPolicy(root, allowed, false)}
	RegisterHandlers(sctx)
	if err := b.Execute(cmd); err != nil {
		t.Fatalf("EnterGoal: %v", err)
	}
	select {
	case <-runEnded:
		b.Drain(time.Second)
	case <-time.After(2 * time.Second):
		t.Fatal("no RunEnded")
	}
	return sctx
}

// Evidence and verify.json run in STATE.md's WORKDIR when there is no --cwd.
func TestReview_EvidenceAndVerifyJSONUseStateWorkDir(t *testing.T) {
	root, wt := t.TempDir(), t.TempDir()
	gitInit(t, wt)
	mustPR167(t, os.MkdirAll(filepath.Join(wt, ".moa"), 0o755))
	mustPR167(t, os.WriteFile(filepath.Join(wt, ".moa", "verify.json"), []byte(`{"checks":[{"name":"where","command":"echo here > ran-here; false"}]}`), 0o644))
	state := filepath.Join(root, goal.DefaultStatePath)
	mustPR167(t, os.MkdirAll(filepath.Dir(state), 0o755))
	mustPR167(t, os.WriteFile(state, []byte("WORKDIR: "+wt+"\n"), 0o644))
	sctx := enter(t, root, []string{wt}, EnterGoal{Objective: "x"})
	dir := goalWorkDir(sctx, sctx.Goal.Info())
	ev, gate := buildGoalEvidence(context.Background(), dir, "")
	real, _ := filepath.EvalSymlinks(wt)
	if !strings.Contains(ev, "EVALUATED IN: "+real) {
		t.Errorf("evidence not in worktree:\n%s", ev)
	}
	if !gate.hasConfig || gate.allPass {
		t.Errorf("gate = %+v, want worktree verify.json red", gate)
	}
	if _, err := os.Stat(filepath.Join(wt, "ran-here")); err != nil {
		t.Errorf("verify.json check did not run in worktree: %v", err)
	}
}

// With --cwd, the STATE.md location must be what it was on main: inside --cwd.
func TestReview_CwdFlagKeepsStateInsideCwd(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "wt")
	mustPR167(t, os.MkdirAll(wt, 0o755))
	sctx := enter(t, root, nil, EnterGoal{Objective: "x", WorkDir: wt})
	info := sctx.Goal.Info()
	real, _ := filepath.EvalSymlinks(wt)
	if want := filepath.Join(real, goal.DefaultStatePath); info.StatePath != want {
		t.Errorf("StatePath = %q, want %q (main behaviour)", info.StatePath, want)
	}
	if info.WorkDir != real {
		t.Errorf("WorkDir = %q, want %q", info.WorkDir, real)
	}
}

// Nonexistent and outside-policy WORKDIR fall back to the session CWD.
func TestReview_BadStateWorkDirFallsBack(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	gitInit(t, root)
	for _, wd := range []string{filepath.Join(root, "missing"), outside, "relative/dir"} {
		mustPR167(t, os.RemoveAll(filepath.Join(root, ".moa")))
		state := filepath.Join(root, goal.DefaultStatePath)
		mustPR167(t, os.MkdirAll(filepath.Dir(state), 0o755))
		mustPR167(t, os.WriteFile(state, []byte("WORKDIR: "+wd+"\n"), 0o644))
		sctx := enter(t, root, nil, EnterGoal{Objective: "x"})
		if got := goalWorkDir(sctx, sctx.Goal.Info()); got != root {
			t.Errorf("WORKDIR %q: goalWorkDir = %q, want fallback %q", wd, got, root)
		}
		if sctx.Goal.LastCommit() != repoHeads(context.Background(), root) {
			t.Errorf("WORKDIR %q: baseline not from session CWD", wd)
		}
	}
}

func mustPR167(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
