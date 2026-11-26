package bus

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/goal"
	"github.com/e-aleixandre/moa/pkg/tool"
)

func TestEnterGoalResolvesRelativeStatePathPerSessionCWD(t *testing.T) {
	cwdA := t.TempDir()
	cwdB := t.TempDir()

	for _, tc := range []struct {
		name      string
		cwd       string
		statePath string
	}{
		{name: "default path", cwd: cwdA},
		{name: "custom relative path", cwd: cwdB, statePath: "nested/STATE.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewLocalBus()
			defer b.Close()

			runEnded := make(chan RunEnded, 1)
			b.Subscribe(func(e RunEnded) { runEnded <- e })
			sctx := &SessionContext{
				SessionID:  tc.name,
				SessionCtx: context.Background(),
				Bus:        b,
				Agent:      &fakeAgent{sendErr: errors.New("stop after setup")},
				State:      NewStateMachine(b, tc.name),
				Goal:       goal.New(),
				CWD:        tc.cwd,
			}
			RegisterHandlers(sctx)

			if err := b.Execute(EnterGoal{Objective: "keep state separate", StatePath: tc.statePath}); err != nil {
				t.Fatalf("EnterGoal: %v", err)
			}

			relativePath := tc.statePath
			if relativePath == "" {
				relativePath = goal.DefaultStatePath
			}
			want := filepath.Join(tc.cwd, relativePath)
			if got := sctx.Goal.Info().StatePath; got != want {
				t.Errorf("StatePath = %q, want %q", got, want)
			}
			if _, err := os.Stat(want); err != nil {
				t.Fatalf("STATE.md not created at %q: %v", want, err)
			}

			select {
			case <-runEnded:
				b.Drain(time.Second)
			case <-time.After(time.Second):
				t.Fatal("goal setup run did not finish")
			}
		})
	}
}

func TestGoalWorkDir_UsesAllowedStateWorkDir(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "feature")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "STATE.md")
	if err := os.WriteFile(statePath, []byte("WORKDIR: "+workDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sctx := &SessionContext{CWD: root, PathPolicy: tool.NewPathPolicy(root, nil, false)}
	if got := goalWorkDir(sctx, goal.Info{StatePath: statePath}); got != workDir {
		t.Errorf("goalWorkDir() = %q, want %q", got, workDir)
	}
}

func TestGoalWorkDir_FallsBackForStateWorkDirOutsidePolicy(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	statePath := filepath.Join(root, "STATE.md")
	if err := os.WriteFile(statePath, []byte("WORKDIR: "+outside+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sctx := &SessionContext{CWD: root, PathPolicy: tool.NewPathPolicy(root, nil, false)}
	if got := goalWorkDir(sctx, goal.Info{StatePath: statePath}); got != root {
		t.Errorf("goalWorkDir() = %q, want fallback %q", got, root)
	}
}

// The maker writes WORKDIR: into STATE.md after EnterGoal without --cwd. The
// goal must then evaluate in that directory: the baseline commit and every
// later evaluation point resolve through goalWorkDir.
func TestEnterGoalHonoursStateWorkDirWithoutCwdFlag(t *testing.T) {
	root := t.TempDir()
	worktree := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = worktree
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	statePath := filepath.Join(root, goal.DefaultStatePath)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("WORKDIR: "+worktree+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := NewLocalBus()
	defer b.Close()
	runEnded := make(chan RunEnded, 1)
	b.Subscribe(func(e RunEnded) { runEnded <- e })
	sctx := &SessionContext{
		SessionID:  "state-workdir",
		SessionCtx: context.Background(),
		Bus:        b,
		Agent:      &fakeAgent{sendErr: errors.New("stop after setup")},
		State:      NewStateMachine(b, "state-workdir"),
		Goal:       goal.New(),
		CWD:        root,
		PathPolicy: tool.NewPathPolicy(root, []string{worktree}, false),
	}
	RegisterHandlers(sctx)

	if err := b.Execute(EnterGoal{Objective: "work elsewhere"}); err != nil {
		t.Fatalf("EnterGoal: %v", err)
	}
	info := sctx.Goal.Info()
	if info.WorkDir != "" {
		t.Errorf("Info.WorkDir = %q, want empty so STATE.md's WORKDIR is reachable", info.WorkDir)
	}
	real, _ := filepath.EvalSymlinks(worktree)
	if got := goalWorkDir(sctx, info); got != real {
		t.Errorf("goalWorkDir = %q, want %q", got, real)
	}
	want := repoHeads(context.Background(), real)
	if want == "" {
		t.Fatal("control: worktree has no HEAD")
	}
	if got := sctx.Goal.LastCommit(); got != want {
		t.Errorf("baseline commit = %q, want the worktree's %q", got, want)
	}

	select {
	case <-runEnded:
		b.Drain(time.Second)
	case <-time.After(time.Second):
		t.Fatal("goal setup run did not finish")
	}
}
