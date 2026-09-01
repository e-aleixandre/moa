package bus

import (
	"context"
	"errors"
	"os"
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
