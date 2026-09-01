package bus

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/goal"
	"github.com/e-aleixandre/moa/pkg/session"
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

// errProvider always fails Stream — simulates a persistently unreachable
// verifier (every retry attempt fails).
type errProvider struct{}

func (errProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	return nil, context.DeadlineExceeded
}

// countingProvider is a verdictProvider that counts how many times it's asked
// to Stream, so a test can prove the verifier was (not) invoked.
type countingProvider struct {
	calls *atomic.Int32
	text  string
}

func (p countingProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.calls.Add(1)
	return verdictProvider{text: p.text}.Stream(ctx, req)
}

func newGoalDriverContext(b EventBus, agent AgentController, verdictJSON string) *SessionContext {
	sctx := &SessionContext{
		SessionID:  "test-session",
		SessionCtx: context.Background(),
		Bus:        b,
		Agent:      agent,
		State:      NewStateMachine(b, "test-session"),
		Goal:       goal.New(),
		// The sandboxed verifier requires a WorkDir; the driver falls back to the
		// session CWD when the goal has none, so give the test one that exists.
		CWD:             os.TempDir(),
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

// TestGoalDriver_PublishesVerifyStartEnd checks the verifying-phase signal is
// emitted around the verifier so the UI can show a "verifying…" indicator.
func TestGoalDriver_PublishesVerifyStartEnd(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newGoalDriverContext(b, fa, `{"satisfied":true,"feedback":"done"}`)
	RegisterHandlers(sctx)

	startCh := make(chan GoalVerifyStarted, 4)
	b.Subscribe(func(e GoalVerifyStarted) { startCh <- e })
	endCh := make(chan GoalVerifyEnded, 4)
	b.Subscribe(func(e GoalVerifyEnded) { endCh <- e })
	endedCh := make(chan GoalEnded, 4)
	b.Subscribe(func(e GoalEnded) { endedCh <- e })

	enterTestGoal(t, sctx, goal.Options{})
	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1, FinalText: "did work"})

	// GoalVerifyStarted is published synchronously before the verify goroutine;
	// GoalVerifyEnded fires from the goroutine's defer, after the terminal
	// GoalEnded — so wait for it explicitly rather than assuming ordering.
	start := drainChan(startCh, b, t)
	if start.Iteration == 0 {
		t.Fatal("GoalVerifyStarted should carry the iteration number")
	}
	_ = drainChan(endedCh, b, t)
	end := drainChan(endCh, b, t)
	// With a single verification (no overlap), the aggregate verifying state
	// after it finishes is false — so the UI clears its indicator (P7).
	if end.Verifying {
		t.Fatal("GoalVerifyEnded.Verifying should be false when no other verification is running")
	}
}

// TestGoalDriver_PersistsMarkers is the regression guard for: goal
// lifecycle events must leave a persistent record in the conversation (role
// "goal") so they survive a reload — not just ephemeral in-memory frontend
// state. A finite satisfied goal produces a start marker, an iteration marker,
// and an end marker. It wires a real Tree + TreeSyncer so the assertion is on
// the persisted tree (what a reload rebuilds from), not just agent state, and
// checks the markers are excluded from the LLM context.
func TestGoalDriver_PersistsMarkers(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newGoalDriverContext(b, fa, `{"satisfied":true,"feedback":"all green"}`)
	sctx.Tree = session.NewTree()
	RegisterHandlers(sctx)
	RegisterTreeSyncer(b, sctx)

	// The end marker is appended inside stopGoal, after GoalEnded is published,
	// on the driver goroutine. Wait for its CommandExecuted so the assertion is
	// deterministic rather than racing the async append.
	endMarker := make(chan struct{}, 4)
	b.Subscribe(func(e CommandExecuted) {
		if e.Command != "goal" {
			return
		}
		for _, m := range e.Messages {
			if m.Role == "goal" {
				if phase, _ := m.Custom["phase"].(string); phase == "end" {
					endMarker <- struct{}{}
					return
				}
			}
		}
	})

	if err := b.Execute(EnterGoal{
		SessionID: "test-session",
		Objective: "make the build green",
		StatePath: filepath.Join(t.TempDir(), "STATE.md"),
	}); err != nil {
		t.Fatalf("EnterGoal: %v", err)
	}
	// EnterGoal's first kick starts a real run whose RunEnded{RunGen:1} drives
	// the loop. Publishing a manual one here would duplicate it and spawn two
	// concurrent verifications, racing a second "iteration" marker past the
	// "end" marker (flaky "last marker phase = iteration, want end").

	select {
	case <-endMarker:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the end goal marker")
	}
	b.Drain(time.Second)

	// Assert on the persisted tree — this is what a reopen rebuilds from.
	var goalMarkers []core.AgentMessage
	for _, m := range sctx.Tree.AllMessages() {
		if m.Role != "goal" {
			continue
		}
		goalMarkers = append(goalMarkers, m)
		if m.IsLLMMessage() {
			t.Fatalf("goal marker must not be an LLM message: %+v", m)
		}
	}
	if len(goalMarkers) < 3 {
		t.Fatalf("expected at least 3 goal markers (start, iteration, end) in the tree, got %d: %+v", len(goalMarkers), goalMarkers)
	}

	phase := func(m core.AgentMessage) string { s, _ := m.Custom["phase"].(string); return s }
	if got := phase(goalMarkers[0]); got != "start" {
		t.Fatalf("first marker phase = %q, want start", got)
	}
	if got := phase(goalMarkers[len(goalMarkers)-1]); got != "end" {
		t.Fatalf("last marker phase = %q, want end", got)
	}
	sawIteration := false
	for _, m := range goalMarkers {
		if phase(m) == "iteration" {
			sawIteration = true
		}
	}
	if !sawIteration {
		t.Fatal("expected an iteration-phase goal marker")
	}

	// The markers must be excluded from the LLM context the tree builds.
	llm, _ := sctx.Tree.BuildContext()
	for _, m := range llm {
		if m.Role == "goal" {
			t.Fatalf("goal marker leaked into LLM context: %+v", m)
		}
	}
}

// TestAppendGoalMarker_DeferredWhenBusy covers the busy → RunEnded deferral: if
// AppendMessage is rejected because a run is live, appendGoalMarker must retry
// once on the next RunEnded and then publish, appending the marker exactly once.
func TestAppendGoalMarker_DeferredWhenBusy(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	// First AppendMessage fails (busy); the deferred retry on RunEnded succeeds.
	fa := &fakeAgent{appendBusy: 1}
	sctx := newTestSessionContext(b, fa)

	cmdCh := make(chan CommandExecuted, 4)
	b.Subscribe(func(e CommandExecuted) {
		if e.Command == "goal" {
			cmdCh <- e
		}
	})

	appendGoalMarker(sctx, "🎯 Goal ended: stopped by user", map[string]any{"phase": "end"})

	// Nothing appended yet (the agent was busy) and no CommandExecuted published.
	b.Drain(500 * time.Millisecond)
	select {
	case <-cmdCh:
		t.Fatal("marker published while agent was busy; should have deferred")
	default:
	}
	if got := len(fa.Messages()); got != 0 {
		t.Fatalf("agent messages = %d, want 0 before RunEnded", got)
	}

	// The run ends: the deferred append fires exactly once.
	b.Publish(RunEnded{SessionID: "test-session"})
	select {
	case <-cmdCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the deferred goal marker")
	}
	// A second RunEnded must NOT append the marker again.
	b.Publish(RunEnded{SessionID: "test-session"})
	b.Drain(500 * time.Millisecond)

	var goalCount int
	for _, m := range fa.Messages() {
		if m.Role == "goal" {
			goalCount++
		}
	}
	if goalCount != 1 {
		t.Fatalf("goal markers appended = %d, want exactly 1", goalCount)
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
	// Unsatisfied verdict: with MaxIterations=1 the loop verifies the first run
	// and then stops (the cap is checked AFTER the verdict, so all N iterations
	// are actually verified rather than running an unverified N+1th).
	sctx := newGoalDriverContext(b, fa, `{"satisfied":false,"feedback":"x"}`)
	RegisterHandlers(sctx)

	iterCh := make(chan GoalIterationEnded, 4)
	b.Subscribe(func(e GoalIterationEnded) { iterCh <- e })
	endedCh := make(chan GoalEnded, 4)
	b.Subscribe(func(e GoalEnded) { endedCh <- e })

	enterTestGoal(t, sctx, goal.Options{MaxIterations: 1})

	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1, FinalText: "attempt"})

	// The single allowed iteration is verified before the backstop fires.
	iter := drainChan(iterCh, b, t)
	if iter.Iteration != 1 {
		t.Fatalf("expected iteration 1 to be verified, got %d", iter.Iteration)
	}
	ended := drainChan(endedCh, b, t)
	if !strings.Contains(ended.Reason, "max iterations") {
		t.Fatalf("expected a max-iterations stop reason, got %q", ended.Reason)
	}
	if sctx.Goal.Active() {
		t.Fatal("goal should stop after reaching max iterations")
	}
}

func TestGoalDriver_VerifierError_PausesWithoutRelaunch(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	// A provider that always fails the Stream: the verifier can't reach a verdict.
	sctx := &SessionContext{
		SessionID:       "test-session",
		SessionCtx:      context.Background(),
		Bus:             b,
		Agent:           fa,
		State:           NewStateMachine(b, "test-session"),
		Goal:            goal.New(),
		ProviderFactory: func(core.Model) (core.Provider, error) { return errProvider{}, nil },
	}
	sctx.RunGenAtomic.Store(1)
	RegisterHandlers(sctx)

	iterCh := make(chan GoalIterationEnded, 4)
	b.Subscribe(func(e GoalIterationEnded) { iterCh <- e })
	endedCh := make(chan GoalEnded, 4)
	b.Subscribe(func(e GoalEnded) { endedCh <- e })

	enterTestGoal(t, sctx, goal.Options{})

	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1, FinalText: "did work"})

	iter := drainChan(iterCh, b, t)
	if iter.Err == nil {
		t.Fatal("a verifier failure must surface as GoalIterationEnded.Err")
	}
	if iter.Satisfied {
		t.Fatal("a verifier failure is not a satisfied verdict")
	}
	ended := drainChan(endedCh, b, t)
	if !strings.Contains(ended.Reason, "verifier unavailable") {
		t.Fatalf("expected a 'verifier unavailable' pause, got %q", ended.Reason)
	}
	if sctx.Goal.Active() {
		t.Fatal("goal should pause (stop) when the verifier is unavailable")
	}
	// The maker must NOT be relaunched with a cryptic error as feedback.
	if fa.wasSendCalled() {
		t.Fatal("verifier failure must not relaunch the maker")
	}
}

// TestGoalDriver_ExhaustedVerdict_PausesWithoutRecording is the regression
// guard for the day-long goal that burned iterations 4-9: a verifier that never
// reached a verdict (it ran out of inspection turns, twice) must NOT be treated
// as "not done yet". The goal pauses, the maker is not relaunched, and nothing
// is recorded as memory for a later verification.
func TestGoalDriver_ExhaustedVerdict_PausesWithoutRecording(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newGoalDriverContext(b, fa, "")
	dir := t.TempDir()
	sctx.CWD = dir
	// A verifier that only ever calls tools: it exhausts its turns in both
	// rounds and reaches no verdict.
	sctx.ProviderFactory = func(core.Model) (core.Provider, error) {
		return toolLoopProvider{dir: dir}, nil
	}
	RegisterHandlers(sctx)

	iterCh := make(chan GoalIterationEnded, 4)
	b.Subscribe(func(e GoalIterationEnded) { iterCh <- e })
	endedCh := make(chan GoalEnded, 4)
	b.Subscribe(func(e GoalEnded) { endedCh <- e })

	enterTestGoal(t, sctx, goal.Options{WorkDir: dir})
	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1, FinalText: "did work", Cost: 0.25})

	iter := drainChan(iterCh, b, t)
	if iter.Satisfied {
		t.Fatal("an exhausted verifier is not a satisfied verdict")
	}
	if !strings.Contains(iter.Feedback, "could not reach a verdict") {
		t.Fatalf("feedback should say no verdict was reached, got %q", iter.Feedback)
	}
	ended := drainChan(endedCh, b, t)
	if !strings.Contains(ended.Reason, "verifier exhausted (paused)") {
		t.Fatalf("expected a 'verifier exhausted (paused)' stop, got %q", ended.Reason)
	}
	if sctx.Goal.Active() {
		t.Fatal("goal should pause when the verifier can't reach a verdict")
	}
	// The maker must NOT be relaunched with a non-verdict as "Continue".
	if fa.wasSendCalled() {
		t.Fatal("an exhausted verifier must not relaunch the maker")
	}
	// And nothing is recorded: a non-verdict fed to the next verification as
	// EARLIER VERIFICATIONS is exactly what made the loop compound its mistake.
	if prior := sctx.Goal.PriorVerdicts(); len(prior) != 0 {
		t.Fatalf("an exhausted verifier must record no verdict, got %+v", prior)
	}
	// The maker's run cost is still charged to the goal budget.
	if got := sctx.Goal.Spent(); got < 0.25 {
		t.Fatalf("budget accounting must still happen, spent = %v", got)
	}
}

// TestGoalExhaustedMarkerText: the UI wording for an unreachable verdict is
// distinct from "not done yet" — the work wasn't judged unfinished, it wasn't
// judged at all.
func TestGoalExhaustedMarkerText(t *testing.T) {
	got := goalExhaustedMarkerText(4, "the verifier ran out of turns twice")
	if !strings.HasPrefix(got, "🎯 Goal iteration 4 — verifier could not reach a verdict") {
		t.Fatalf("unexpected marker text: %q", got)
	}
	if strings.Contains(goalIterationMarkerText(4, false, ""), "could not reach") {
		t.Fatal("the ordinary unsatisfied marker must keep its own wording")
	}
}

// toolLoopProvider always answers with a tool call, so the verifier agent never
// produces a verdict and exhausts its turn budget in every round.
type toolLoopProvider struct{ dir string }

func (p toolLoopProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	ch := make(chan core.AssistantEvent, 2)
	// A unique tool-call id and path keep the doom-loop guard out of the way so
	// the max-turns cap is what ends each round.
	id := fmt.Sprintf("call_%d", time.Now().UnixNano())
	msg := core.Message{
		Role:       "assistant",
		Content:    []core.Content{core.ToolCallContent(id, "ls", map[string]any{"path": p.dir})},
		StopReason: "tool_use",
		Usage:      &core.Usage{Input: 10, Output: 5, TotalTokens: 15},
	}
	go func() {
		defer close(ch)
		ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
		ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	}()
	return ch, nil
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
	// A large send delay parks the relaunched run so it can't cascade into
	// further iterations (and trip the stall guard, which would restore the
	// budget) before we observe the capped value.
	fa := &fakeAgent{sendDelay: time.Hour}
	sctx := newGoalDriverContext(b, fa, `{"satisfied":false,"feedback":"more"}`)
	RegisterHandlers(sctx)

	iterCh := make(chan GoalIterationEnded, 4)
	b.Subscribe(func(e GoalIterationEnded) { iterCh <- e })

	enterTestGoal(t, sctx, goal.Options{TotalBudget: 10.0})

	// A partial-cost run leaves budget: the next iteration is capped at the
	// remaining pool (10 - 3 = 7) so cumulative spend can't exceed the total.
	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1, FinalText: "partial", Cost: 3.0})

	_ = drainChan(iterCh, b, t) // wait for the verdict to be processed
	// The relaunch's SetMaxBudget runs async after the verdict. Poll until it
	// lands, draining the bus each tick.
	deadline := time.After(10 * time.Second)
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

	ev, _ := buildGoalEvidence(context.Background(), dir, "I changed the file")
	if !strings.HasPrefix(ev, "EVALUATED IN: "+dir+"\n") {
		t.Fatalf("evidence should identify its directory, got:\n%s", ev)
	}
	if !strings.Contains(ev, "DIFF vs HEAD") || !strings.Contains(ev, "MODIFIED CONTENT") {
		t.Fatalf("evidence should contain the real diff, got:\n%s", ev)
	}
	// No .moa/verify.json → checks reported as "not run", not silently omitted.
	if !strings.Contains(ev, "AUTOMATED CHECKS: not run") {
		t.Fatalf("evidence should note that checks were not run, got:\n%s", ev)
	}
}

// TestRepoHeads_SeesWorktreeCommits is the guard for the stalled-detection fix:
// the maker often works inside a git worktree it created, so HEAD of the goal's
// own directory never moves. repoHeads fingerprints every worktree's HEAD, so a
// commit made in the linked worktree registers as progress.
func TestRepoHeads_SeesWorktreeCommits(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(wd string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = wd
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(main, "init")
	git(main, "config", "user.email", "t@t.t")
	git(main, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(main, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(main, "add", ".")
	git(main, "commit", "-m", "init")

	// A sibling worktree, as the maker creates under the parent of the cwd.
	wt := filepath.Join(dir, "feature")
	git(main, "worktree", "add", "-b", "feature", wt)

	before := repoHeads(context.Background(), main)
	if before == "" {
		t.Fatal("repoHeads should report the worktree HEADs")
	}

	// The commit lands in the worktree; main's own HEAD is untouched.
	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(wt, "config", "user.email", "t@t.t")
	git(wt, "config", "user.name", "t")
	git(wt, "commit", "-am", "work in the worktree")

	mainHead := runGit(context.Background(), main, "rev-parse", "HEAD")
	if mainHead == "" {
		t.Fatal("expected a HEAD in the main worktree")
	}
	after := repoHeads(context.Background(), main)
	if after == before {
		t.Fatalf("a commit in a linked worktree must change the fingerprint (%q)", after)
	}
	if !strings.Contains(after, mainHead) {
		t.Fatalf("fingerprint should still include the main worktree HEAD, got %q", after)
	}
}

// TestRepoHeads_NotARepo: outside a git repository the fingerprint is empty, so
// the driver falls back to HadEdits as the only progress signal.
func TestRepoHeads_NotARepo(t *testing.T) {
	if got := repoHeads(context.Background(), t.TempDir()); got != "" {
		t.Fatalf("expected an empty fingerprint outside a repo, got %q", got)
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

// newGoalRepoWithFailingChecks creates a git repo whose .moa/verify.json defines
// a check that always fails, so the deterministic gate is red. Returns the dir.
func newGoalRepoWithFailingChecks(t *testing.T, verifyJSON string) string {
	t.Helper()
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
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".moa"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".moa", "verify.json"), []byte(verifyJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "init")
	return dir
}

// TestGoalDriver_CheckGate_OverridesSatisfiedVerdict is the core guard for the
// #4 hard gate: when a red .moa/verify.json is present, the goal is NOT done —
// and the LLM verifier is SKIPPED entirely (the free checks already settled it,
// so paying the model just to discard its verdict would burn money). The goal
// keeps going and the failing check output reaches the maker as feedback.
func TestGoalDriver_CheckGate_OverridesSatisfiedVerdict(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	// A large send delay parks the relaunched run so it can't cascade into more
	// iterations while we assert.
	fa := &fakeAgent{sendDelay: time.Hour}
	sctx := newGoalDriverContext(b, fa, `{"satisfied":true,"feedback":"looks great to me"}`)
	// Count verifier invocations: with red checks the verifier must never run.
	var verifierCalls atomic.Int32
	sctx.ProviderFactory = func(core.Model) (core.Provider, error) {
		return countingProvider{calls: &verifierCalls, text: `{"satisfied":true,"feedback":"looks great to me"}`}, nil
	}
	// A check that always fails: the gate must be red.
	dir := newGoalRepoWithFailingChecks(t, `{"checks":[{"name":"test","command":"exit 1"}]}`)
	sctx.CWD = dir
	RegisterHandlers(sctx)

	iterCh := make(chan GoalIterationEnded, 4)
	b.Subscribe(func(e GoalIterationEnded) { iterCh <- e })
	endedCh := make(chan GoalEnded, 4)
	b.Subscribe(func(e GoalEnded) { endedCh <- e })

	enterTestGoal(t, sctx, goal.Options{WorkDir: dir})
	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1, FinalText: "all done, tests pass", HadEdits: true})

	iter := drainChan(iterCh, b, t)
	if iter.Satisfied {
		t.Fatal("the hard gate must override a satisfied verdict when checks are red")
	}
	if !strings.Contains(iter.Feedback, "checks") && !strings.Contains(iter.Feedback, "test") {
		t.Fatalf("gate feedback should mention the failing checks, got:\n%s", iter.Feedback)
	}
	if n := verifierCalls.Load(); n != 0 {
		t.Fatalf("verifier must be skipped when checks are red (no money burned), but it ran %d time(s)", n)
	}
	expectNone(endedCh, b, t)
	if !sctx.Goal.Active() {
		t.Fatal("goal must stay active (not closed) when the gate is red")
	}
}

// TestGoalDriver_CheckGate_GreenAllowsSatisfied confirms the gate only blocks a
// RED result: with passing checks, a satisfied verdict still closes the goal.
func TestGoalDriver_CheckGate_GreenAllowsSatisfied(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newGoalDriverContext(b, fa, `{"satisfied":true,"feedback":"done"}`)
	dir := newGoalRepoWithFailingChecks(t, `{"checks":[{"name":"test","command":"exit 0"}]}`)
	sctx.CWD = dir
	RegisterHandlers(sctx)

	iterCh := make(chan GoalIterationEnded, 4)
	b.Subscribe(func(e GoalIterationEnded) { iterCh <- e })
	endedCh := make(chan GoalEnded, 4)
	b.Subscribe(func(e GoalEnded) { endedCh <- e })

	enterTestGoal(t, sctx, goal.Options{WorkDir: dir})
	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1, FinalText: "done", HadEdits: true})

	iter := drainChan(iterCh, b, t)
	if !iter.Satisfied {
		t.Fatalf("green checks + satisfied verdict must close the goal, got %+v", iter)
	}
	ended := drainChan(endedCh, b, t)
	if ended.Reason != "objective met" {
		t.Fatalf("expected 'objective met', got %q", ended.Reason)
	}
}

// TestGoalDriver_RecordsVerdictMemory verifies the verifier's memory is
// accumulated across iterations: each finished verdict is recorded so the next
// verification can be reminded of it.
func TestGoalDriver_RecordsVerdictMemory(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{sendDelay: time.Hour}
	sctx := newGoalDriverContext(b, fa, `{"satisfied":false,"feedback":"phase 2 missing in api.go"}`)
	RegisterHandlers(sctx)

	iterCh := make(chan GoalIterationEnded, 4)
	b.Subscribe(func(e GoalIterationEnded) { iterCh <- e })

	enterTestGoal(t, sctx, goal.Options{MaxStalled: 5})
	b.Publish(RunEnded{SessionID: "test-session", RunGen: 1, FinalText: "did phase 1", HadEdits: true})

	_ = drainChan(iterCh, b, t)
	prior := sctx.Goal.PriorVerdicts()
	if len(prior) != 1 {
		t.Fatalf("expected 1 recorded verdict, got %d", len(prior))
	}
	if prior[0].Satisfied {
		t.Fatal("recorded verdict should be unsatisfied")
	}
	if !strings.Contains(prior[0].Feedback, "phase 2 missing") {
		t.Fatalf("recorded feedback should carry the verifier's note, got %q", prior[0].Feedback)
	}
}

// TestSummarizePriorVerdicts checks the memo formatting handed to the verifier.
func TestSummarizePriorVerdicts(t *testing.T) {
	if got := summarizePriorVerdicts(nil); got != "" {
		t.Fatalf("empty history should summarize to empty, got %q", got)
	}
	got := summarizePriorVerdicts([]goal.IterationVerdict{
		{Iteration: 1, Satisfied: false, Feedback: "missing X\nand Y"},
		{Iteration: 2, Satisfied: false, Feedback: "still missing Y"},
	})
	if !strings.Contains(got, "Iteration 1: not satisfied") || !strings.Contains(got, "Iteration 2: not satisfied") {
		t.Fatalf("summary should list each iteration, got:\n%s", got)
	}
	if !strings.Contains(got, "missing X") || !strings.Contains(got, "still missing Y") {
		t.Fatalf("summary should carry each feedback, got:\n%s", got)
	}
}
