package bus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/goal"
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
