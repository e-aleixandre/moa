package bus

import (
	"sync/atomic"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/goal"
)

// A goal run that ends while a question waits (open, or answered but not read
// yet) is neither success nor a reason to relaunch: no verifier call, no
// iteration, no relaunch. When nothing waits any more the next run end is
// verified as usual.
func TestGoalDriver_YieldsWhileAQuestionWaits(t *testing.T) {
	cases := []struct {
		name  string
		hold  func(sctx *SessionContext) (release func())
		label string
	}{
		{"open question", func(sctx *SessionContext) func() {
			sctx.Approvals.AddAsync(PendingAskInfo{ID: "q_1", Questions: []AskQuestion{{Text: "ship?"}}})
			return func() { sctx.Approvals.DropAsync("q_1") }
		}, "open"},
		{"answer not read yet", func(sctx *SessionContext) func() {
			var owed atomic.Bool
			owed.Store(true)
			sctx.AnswerWorkOwed = owed.Load
			return func() { owed.Store(false) }
		}, "owed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewLocalBus()
			defer b.Close()
			fa := &fakeAgent{}
			var calls atomic.Int32
			sctx := newGoalDriverContext(b, fa, `{"satisfied":false,"feedback":"keep going"}`)
			sctx.ProviderFactory = func(core.Model) (core.Provider, error) {
				return countingProvider{calls: &calls, text: `{"satisfied":false,"feedback":"keep going"}`}, nil
			}
			sctx.Approvals = NewApprovalManager(b, sctx.State, sctx.SessionID)
			RegisterHandlers(sctx)

			iterCh := make(chan GoalIterationEnded, 4)
			b.Subscribe(func(e GoalIterationEnded) { iterCh <- e })
			enterTestGoal(t, sctx, goal.Options{})

			release := tc.hold(sctx)
			b.Publish(RunEnded{SessionID: sctx.SessionID, RunGen: 1, FinalText: "asked, waiting"})
			expectNone(iterCh, b, t)
			if got := sctx.Goal.Info().Iteration; got != 0 {
				t.Fatalf("a waiting question must not consume an iteration, got %d", got)
			}
			if calls.Load() != 0 {
				t.Fatalf("the verifier ran %d time(s) while a question waited", calls.Load())
			}
			if !sctx.Goal.Active() {
				t.Fatal("yielding must leave the goal active")
			}

			release()
			b.Publish(RunEnded{SessionID: sctx.SessionID, RunGen: 1, FinalText: "answered, done reading"})
			if it := drainChan(iterCh, b, t); it.Iteration != 1 {
				t.Fatalf("after the question was settled the run end must be verified, got %+v", it)
			}
		})
	}
}
