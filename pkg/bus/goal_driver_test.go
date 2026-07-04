package bus

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/goal"
)

// verdictProvider streams a fixed assistant text (used as the verifier's reply).
type verdictProvider struct{ text string }

func (p verdictProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	ch := make(chan core.AssistantEvent, 2)
	go func() {
		defer close(ch)
		msg := core.Message{
			Role:       "assistant",
			Content:    []core.Content{core.TextContent(p.text)},
			StopReason: "end_turn",
		}
		ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: p.text}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	}()
	return ch, nil
}

func newGoalDriverContext(b EventBus, agent AgentController, verdictJSON string) *SessionContext {
	sctx := &SessionContext{
		SessionID:       "test-session",
		SessionCtx:      context.Background(),
		Bus:             b,
		Agent:           agent,
		State:           NewStateMachine(b, "test-session"),
		Goal:            goal.New(),
		ProviderFactory: func(core.Model) (core.Provider, error) { return verdictProvider{text: verdictJSON}, nil },
	}
	// The driver treats a RunEnded whose RunGen != the current generation as
	// stale; align them so the manually-published event is considered live.
	sctx.RunGenAtomic.Store(1)
	return sctx
}

func enterTestGoal(t *testing.T, sctx *SessionContext, opts goal.Options) {
	t.Helper()
	opts.StatePath = filepath.Join(t.TempDir(), "STATE.md")
	if opts.Objective == "" {
		opts.Objective = "make the build green"
	}
	if err := sctx.Goal.Enter(opts); err != nil {
		t.Fatalf("Enter: %v", err)
	}
}

func TestGoalDriver_FiniteSatisfied_Stops(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newGoalDriverContext(b, fa, `{"satisfied":true,"feedback":"all green"}`)
	RegisterHandlers(sctx)

	iterCh := make(chan GoalIterationEnded, 4)
	b.Subscribe(func(e GoalIterationEnded) { iterCh <- e })
	endedCh := make(chan GoalEnded, 4)
	b.Subscribe(func(e GoalEnded) { endedCh <- e })

	enterTestGoal(t, sctx, goal.Options{})
	_ = fa.SetCompactAt(260000) // as EnterGoal would have

	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1, FinalText: "I made the build pass"})

	iter := drainChan(iterCh, b, t)
	if !iter.Satisfied {
		t.Fatalf("expected satisfied verdict, got %+v", iter)
	}
	ended := drainChan(endedCh, b, t)
	if ended.Reason != "objective met" {
		t.Fatalf("expected 'objective met', got %q", ended.Reason)
	}
	if sctx.Goal.Active() {
		t.Fatal("goal should be inactive after a finite success")
	}
	if fa.compactAt != 0 {
		t.Fatalf("compaction threshold should be restored to 0, got %d", fa.compactAt)
	}
}

func TestGoalDriver_Unsatisfied_StallGuardStops(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newGoalDriverContext(b, fa, `{"satisfied":false,"feedback":"tests still failing"}`)
	RegisterHandlers(sctx)

	endedCh := make(chan GoalEnded, 4)
	b.Subscribe(func(e GoalEnded) { endedCh <- e })

	enterTestGoal(t, sctx, goal.Options{MaxStalled: 1})

	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1, FinalText: "hmm"})

	ended := drainChan(endedCh, b, t)
	if ended.Reason == "" {
		t.Fatal("expected a stop reason for the stall guard")
	}
	if sctx.Goal.Active() {
		t.Fatal("goal should stop after hitting the stall guard")
	}
}

func TestGoalDriver_ProgressResetsStall(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	// A large send delay parks the relaunched run so it can't cascade into
	// further iterations while we assert.
	fa := &fakeAgent{sendDelay: time.Hour}
	sctx := newGoalDriverContext(b, fa, `{"satisfied":false,"feedback":"keep going"}`)
	RegisterHandlers(sctx)

	endedCh := make(chan GoalEnded, 4)
	b.Subscribe(func(e GoalEnded) { endedCh <- e })

	enterTestGoal(t, sctx, goal.Options{MaxStalled: 1})

	// Not done yet, but the iteration made edits — that's forward progress, not a
	// stall, so the loop must continue even though MaxStalled is 1.
	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1, HadEdits: true})

	expectNone(endedCh, b, t)
	if !sctx.Goal.Active() {
		t.Fatal("a productive iteration must keep the goal running")
	}
	if got := sctx.Goal.Info().Stalled; got != 0 {
		t.Fatalf("progress should keep the stall counter at 0, got %d", got)
	}
}

func TestGoalDriver_MaxIterations_Stops(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	// Verdict never matters — the iteration backstop trips first.
	sctx := newGoalDriverContext(b, fa, `{"satisfied":false,"feedback":"x"}`)
	RegisterHandlers(sctx)

	endedCh := make(chan GoalEnded, 4)
	b.Subscribe(func(e GoalEnded) { endedCh <- e })

	// Pre-advance the iteration counter so the next BeginIteration exceeds the cap.
	enterTestGoal(t, sctx, goal.Options{MaxIterations: 1})
	sctx.Goal.BeginIteration() // iteration = 1

	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1}) // BeginIteration → 2 > 1

	ended := drainChan(endedCh, b, t)
	if ended.Reason == "" {
		t.Fatal("expected a stop reason for the iteration backstop")
	}
	if sctx.Goal.Active() {
		t.Fatal("goal should stop after reaching max iterations")
	}
}

func TestGoalDriver_BudgetCeiling_Stops(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	// Unsatisfied verdict: the loop would relaunch forever if not for the budget.
	sctx := newGoalDriverContext(b, fa, `{"satisfied":false,"feedback":"keep going"}`)
	RegisterHandlers(sctx)

	endedCh := make(chan GoalEnded, 4)
	b.Subscribe(func(e GoalEnded) { endedCh <- e })

	enterTestGoal(t, sctx, goal.Options{TotalBudget: 5.0})

	// A run whose cost reaches the total budget stops the loop before relaunch.
	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1, FinalText: "spent it all", Cost: 5.0})

	ended := drainChan(endedCh, b, t)
	if !strings.Contains(ended.Reason, "budget") {
		t.Fatalf("expected a budget stop reason, got %q", ended.Reason)
	}
	if sctx.Goal.Active() {
		t.Fatal("goal should stop when the cumulative budget is reached")
	}
}

func TestGoalDriver_BudgetCapsNextIteration(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newGoalDriverContext(b, fa, `{"satisfied":false,"feedback":"more"}`)
	RegisterHandlers(sctx)

	iterCh := make(chan GoalIterationEnded, 4)
	b.Subscribe(func(e GoalIterationEnded) { iterCh <- e })

	enterTestGoal(t, sctx, goal.Options{TotalBudget: 10.0})

	// A partial-cost run leaves budget: the next iteration is capped at the
	// remaining pool (10 - 3 = 7) so cumulative spend can't exceed the total.
	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1, FinalText: "partial", Cost: 3.0})

	_ = drainChan(iterCh, b, t) // wait for the verdict to be processed
	// Generous deadline: the relaunch's SetMaxBudget runs async after the
	// verdict, and this test can be starved under full-suite parallel load.
	deadline := time.After(5 * time.Second)
	for fa.MaxBudget() != 7.0 {
		select {
		case <-deadline:
			t.Fatalf("next-iteration budget = %v, want 7.0 (remaining)", fa.MaxBudget())
		default:
			b.Drain(50 * time.Millisecond)
		}
	}
}

func TestGoalDriver_IgnoresErroredRun(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newGoalDriverContext(b, fa, `{"satisfied":true,"feedback":"x"}`)
	RegisterHandlers(sctx)

	endedCh := make(chan GoalEnded, 4)
	b.Subscribe(func(e GoalEnded) { endedCh <- e })

	enterTestGoal(t, sctx, goal.Options{})

	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1, Err: context.DeadlineExceeded})

	expectNone(endedCh, b, t)
	if !sctx.Goal.Active() {
		t.Fatal("an errored run must not end the goal")
	}
	if got := sctx.Goal.Info().Iteration; got != 0 {
		t.Fatalf("an errored run must not consume an iteration, got %d", got)
	}
}

func TestBuildGoalEvidence_IncludesDiffAndChecks(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	git("config", "user.email", "t@t.t")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "init")
	// A working-tree change the verifier must see.
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("MODIFIED CONTENT\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ev := buildGoalEvidence(context.Background(), dir, "I changed the file")
	if !strings.Contains(ev, "DIFF vs HEAD") || !strings.Contains(ev, "MODIFIED CONTENT") {
		t.Fatalf("evidence should contain the real diff, got:\n%s", ev)
	}
	// No .moa/verify.json → checks reported as "not run", not silently omitted.
	if !strings.Contains(ev, "AUTOMATED CHECKS: not run") {
		t.Fatalf("evidence should note that checks were not run, got:\n%s", ev)
	}
}

func TestGoalDriver_InactiveGoal_NoOp(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newGoalDriverContext(b, fa, `{"satisfied":true,"feedback":"x"}`)
	RegisterHandlers(sctx)

	endedCh := make(chan GoalEnded, 4)
	b.Subscribe(func(e GoalEnded) { endedCh <- e })

	// Goal never entered.
	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1})
	expectNone(endedCh, b, t)
}
