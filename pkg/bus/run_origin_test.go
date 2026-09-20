package bus

import (
	"context"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// Provenance is read off the Custom metadata internal producers already stamp.
// What matters is the line it draws: a turn somebody asked for, versus a
// background job delivering the result of a turn already under way.
func TestOriginFromCustom(t *testing.T) {
	cases := []struct {
		name            string
		custom          map[string]any
		explicit        bool
		continueCurrent bool
		jobs            []string
	}{
		{name: "a typed prompt", custom: nil, explicit: true},
		{name: "the owner writing to a child", custom: map[string]any{"source": "owner"}, explicit: true},
		{name: "a delivered report", custom: map[string]any{"source": "report"}, explicit: true},
		{name: "a heartbeat", custom: map[string]any{"source": "heartbeat"}, explicit: true},
		{
			name:   "a bash job reporting back",
			custom: map[string]any{"source": "bash_job", "bash_job_id": "bash-7"},
			jobs:   []string{"bash-7"},
		},
		{
			name:   "an async subagent reporting back",
			custom: map[string]any{"source": "subagent", "subagent_job_id": "sub-3"},
			jobs:   []string{"sub-3"},
		},
		// Machinery continuing its own work, tied to no job: unknown, which
		// consumers treat as a new turn rather than losing it.
		{name: "the goal loop", custom: map[string]any{"source": "goal"}, continueCurrent: true},
		// The user starting a goal is work they asked for: a new turn, even
		// though every iteration after it continues that same turn.
		{name: "starting a goal", custom: map[string]any{"source": "goal_start"}, explicit: true},
		{name: "auto verify", custom: map[string]any{"source": "auto_verify"}, continueCurrent: true},
		// A notification that lost its ID cannot be folded into a turn: there
		// is nothing to fold it into.
		{name: "a bash job with no id", custom: map[string]any{"source": "bash_job"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := originFromCustom(tc.custom)
			if got.Explicit != tc.explicit {
				t.Fatalf("explicit = %v, want %v", got.Explicit, tc.explicit)
			}
			if got.ContinueCurrent != tc.continueCurrent {
				t.Fatalf("continueCurrent = %v, want %v", got.ContinueCurrent, tc.continueCurrent)
			}
			if len(got.ContinuationOf) != len(tc.jobs) {
				t.Fatalf("continuation = %v, want %v", got.ContinuationOf, tc.jobs)
			}
			for i, id := range tc.jobs {
				if got.ContinuationOf[i] != id {
					t.Fatalf("continuation[%d] = %q, want %q", i, got.ContinuationOf[i], id)
				}
			}
		})
	}
}

// A drained queue batch can mix notifications with something the user typed.
// The instruction is what the run is about, so the whole run is explicit —
// otherwise the user's turn would be folded into a job's continuation and its
// outcome never reported on its own.
func TestOriginFromItems_ExplicitInputWins(t *testing.T) {
	mixed := originFromItems([]core.SteerItem{
		{Custom: map[string]any{"source": "bash_job", "bash_job_id": "bash-7"}},
		{Text: "actually, do this instead"},
	})
	if !mixed.Explicit {
		t.Fatalf("a batch containing a typed message must be explicit: %+v", mixed)
	}
}

func TestOriginFromItems_PureContinuationKeepsEveryJob(t *testing.T) {
	got := originFromItems([]core.SteerItem{
		{Custom: map[string]any{"source": "bash_job", "bash_job_id": "bash-7"}},
		{Custom: map[string]any{"source": "subagent", "subagent_job_id": "sub-3"}},
	})
	if got.Explicit {
		t.Fatalf("notifications alone are not an explicit turn: %+v", got)
	}
	if len(got.ContinuationOf) != 2 || got.ContinuationOf[0] != "bash-7" || got.ContinuationOf[1] != "sub-3" {
		t.Fatalf("continuation = %v, want both job ids", got.ContinuationOf)
	}
}

// The provenance decided at admission must reach subscribers on RunStarted:
// that is the only place a consumer can learn it.
func TestRunStartedCarriesTheAdmittedOrigin(t *testing.T) {
	rt := newTestRuntime(t)
	sctx := rt.Context()
	seen := make(chan RunOrigin, 4)
	unsub := sctx.Bus.Subscribe(func(e RunStarted) { seen <- e.Origin })
	defer unsub()

	if err := startRunWithOrigin(sctx, "continuation",
		RunOrigin{ContinuationOf: []string{"bash-7"}},
		func(ctx context.Context) ([]core.AgentMessage, error) { return nil, nil },
	); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-seen:
		if got.Explicit || len(got.ContinuationOf) != 1 || got.ContinuationOf[0] != "bash-7" {
			t.Fatalf("RunStarted origin = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no RunStarted was published")
	}
}

// A run launched without an origin must not inherit the previous one. The
// pending value is taken, not read.
func TestPendingOriginIsNotInheritedByTheNextRun(t *testing.T) {
	rt := newTestRuntime(t)
	sctx := rt.Context()
	sctx.setPendingRunOrigin(RunOrigin{ContinuationOf: []string{"bash-7"}})
	if got := sctx.takePendingRunOrigin(); len(got.ContinuationOf) != 1 {
		t.Fatalf("the pending origin was not returned: %+v", got)
	}
	if got := sctx.takePendingRunOrigin(); got.Explicit || len(got.ContinuationOf) != 0 {
		t.Fatalf("a second run inherited a stale origin: %+v", got)
	}
}

// BackgroundWork counts the same things quiescence refuses to ignore, so a
// report that stops waiting can say exactly what it stopped waiting for.
func TestBackgroundWorkCountsEverySource(t *testing.T) {
	rt := newTestRuntime(t)
	sctx := rt.Context()
	if got := rt.BackgroundWork(); got != 0 {
		t.Fatalf("a fresh session has %d background work, want 0", got)
	}
	sctx.beginAutoVerify()
	sctx.beginGoalVerify()
	sctx.trackBackgroundEvent(SubagentStarted{JobID: "sub-1"})
	sctx.trackBackgroundEvent(BashJobStarted{JobID: "bash-1"})
	if got := rt.BackgroundWork(); got != 4 {
		t.Fatalf("background work = %d, want 4", got)
	}
	if !sctx.hasBackgroundWork() {
		t.Fatal("hasBackgroundWork disagrees with the count it shares a lock with")
	}
	sctx.endAutoVerify()
	sctx.endGoalVerify()
	sctx.trackBackgroundEvent(SubagentEnded{JobID: "sub-1"})
	sctx.trackBackgroundEvent(BashJobSettled{JobID: "bash-1"})
	if got := rt.BackgroundWork(); got != 0 {
		t.Fatalf("background work = %d after everything ended, want 0", got)
	}
}
