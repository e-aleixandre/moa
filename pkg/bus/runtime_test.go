package bus

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// fakeAgentSubscriber wraps fakeAgent to also implement AgentSubscriber.
type fakeAgentSubscriber struct {
	*fakeAgent
	fakeSubscriber
}

func newFakeAgentSubscriber() *fakeAgentSubscriber {
	return &fakeAgentSubscriber{
		fakeAgent: &fakeAgent{},
	}
}

func TestNewSessionRuntime_Works(t *testing.T) {
	fas := newFakeAgentSubscriber()
	fas.model = core.Model{ID: "claude-4", Name: "Claude 4"}

	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Query model via bus.
	m, err := QueryTyped[GetModel, core.Model](rt.Bus, GetModel{})
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "claude-4" {
		t.Fatalf("Model.ID = %q", m.ID)
	}
}

func TestNewSessionRuntime_NilAgent(t *testing.T) {
	_, err := NewSessionRuntime(RuntimeConfig{})
	if err == nil {
		t.Fatal("expected error for nil Agent")
	}
}

func TestNewSessionRuntime_AutoSubscriber(t *testing.T) {
	// fakeAgentSubscriber implements both AgentController and AgentSubscriber.
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent: fas, // implements both interfaces
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
}

func TestNewSessionRuntime_NoSubscriber(t *testing.T) {
	// fakeAgent does NOT implement AgentSubscriber.
	fa := &fakeAgent{}
	_, err := NewSessionRuntime(RuntimeConfig{
		Agent: fa,
	})
	if err == nil {
		t.Fatal("expected error when Agent doesn't implement AgentSubscriber and no Subscriber provided")
	}
}

func TestSessionRuntime_StateInitiallyIdle(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if rt.State.Current() != StateIdle {
		t.Fatalf("state = %q, want idle", rt.State.Current())
	}
}

func TestSessionRuntime_Close_Idempotent(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}

	rt.Close()
	rt.Close() // should not panic
}

func TestSessionRuntime_Close_AbortsAgent(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}

	rt.Close()
	if !fas.wasAborted() {
		t.Fatal("Abort not called on Close")
	}
}

func TestSessionRuntime_DefaultSessionID(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if rt.ID != "default" {
		t.Fatalf("ID = %q, want 'default'", rt.ID)
	}
}

func TestSessionRuntime_CustomSessionID(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		SessionID:  "custom-123",
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if rt.ID != "custom-123" {
		t.Fatalf("ID = %q", rt.ID)
	}
}

func TestSessionRuntime_FullLifecycle(t *testing.T) {
	fas := newFakeAgentSubscriber()
	fas.sendResult = []core.AgentMessage{
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			{Type: "text", Text: "hello from runtime"},
		}}},
	}

	fp := &fakePersister{}
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
		Persister:  fp,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Subscribe to RunEnded.
	gotRunEnded := make(chan RunEnded, 1)
	rt.Bus.Subscribe(func(e RunEnded) { gotRunEnded <- e })

	// Send prompt.
	if err := rt.Bus.Execute(SendPrompt{Text: "hello"}); err != nil {
		t.Fatal(err)
	}

	// Wait for completion.
	re := waitForRunEnded(t, gotRunEnded, rt.Bus)
	if re.FinalText != "hello from runtime" {
		t.Fatalf("FinalText = %q", re.FinalText)
	}
	if re.Err != nil {
		t.Fatalf("Err = %v", re.Err)
	}

	// State back to idle.
	if rt.State.Current() != StateIdle {
		t.Fatalf("state = %q", rt.State.Current())
	}

	// Persister should have been called.
	// Give persistence reactor time to process.
	rt.Bus.Drain(time.Second)
	time.Sleep(50 * time.Millisecond)
	rt.Bus.Drain(time.Second)
	if fp.count() == 0 {
		t.Fatal("persister was not called")
	}
}

func TestSessionRuntime_FullLifecycle_Error(t *testing.T) {
	fas := newFakeAgentSubscriber()
	fas.sendErr = errors.New("boom")

	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	gotRunEnded := make(chan RunEnded, 1)
	rt.Bus.Subscribe(func(e RunEnded) { gotRunEnded <- e })

	if err := rt.Bus.Execute(SendPrompt{Text: "fail"}); err != nil {
		t.Fatal(err)
	}

	re := waitForRunEnded(t, gotRunEnded, rt.Bus)
	if re.Err == nil || re.Err.Error() != "boom" {
		t.Fatalf("Err = %v", re.Err)
	}
	if rt.State.Current() != StateError {
		t.Fatalf("state = %q, want error", rt.State.Current())
	}
}

func TestSessionRuntime_BridgeForwards(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Verify bridge forwards agent events to bus.
	got := make(chan AgentStarted, 1)
	rt.Bus.Subscribe(func(e AgentStarted) { got <- e })

	fas.emit(core.AgentEvent{Type: core.AgentEventStart})
	rt.Bus.Drain(time.Second)
	select {
	case e := <-got:
		if e.SessionID != "default" {
			t.Fatalf("SessionID = %q", e.SessionID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for bridged event")
	}
}

// TestSessionRuntime_Flush_PersistsLastTurn demonstrates the lost-last-turn
// shutdown fix: a turn that completed just before shutdown must reach disk even
// if the async RunEnded→TreeSynced→save chain never ran. Here RunEnded is never
// published, so the only path to disk is the synchronous Flush.
func TestSessionRuntime_Flush_PersistsLastTurn(t *testing.T) {
	fas := newFakeAgentSubscriber()
	fp := &fakeTreePersister{}
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
		Persister:  fp,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Simulate a turn that just completed: the agent gained messages but no
	// RunEnded (and thus no TreeSynced→save) has fired yet.
	if err := fas.LoadState([]core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{core.TextContent("hi")}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("done")}}},
	}, 0); err != nil {
		t.Fatal(err)
	}
	if fp.treeSnapCount() != 0 {
		t.Fatalf("expected no snapshot before Flush, got %d", fp.treeSnapCount())
	}

	// Flush must fold the turn into the tree and persist it synchronously.
	if err := rt.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if fp.treeSnapCount() != 1 {
		t.Fatalf("expected 1 snapshot after Flush, got %d", fp.treeSnapCount())
	}
	if got := len(fp.lastTree()); got != 2 {
		t.Fatalf("persisted tree = %d entries, want 2 (last turn must be included)", got)
	}
}

func TestSessionRuntime_Flush_NoPersister(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if err := rt.Flush(); err != nil {
		t.Fatalf("Flush with no persister should return nil, got %v", err)
	}
}

func TestSessionRuntime_Context(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	if rt.Context() == nil {
		t.Fatal("Context() returned nil")
	}
	if rt.Context().Bus != rt.Bus {
		t.Fatal("Context().Bus != rt.Bus")
	}
}

func newTestRuntime(t *testing.T) *SessionRuntime {
	t.Helper()
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{
		Agent:      fas.fakeAgent,
		Subscriber: &fas.fakeSubscriber,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	return rt
}

func TestWaitSettled_ReturnsWhenRunEnds(t *testing.T) {
	rt := newTestRuntime(t)
	if err := rt.State.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}

	// Simulate the run goroutine settling shortly after shutdown begins.
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = rt.State.Transition(StateIdle)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	if !rt.WaitSettled(ctx) {
		t.Fatal("WaitSettled = false, want true (run should have settled)")
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("WaitSettled returned too early (%v); it did not wait for the transition", elapsed)
	}
	if s := rt.State.Current(); s != StateIdle {
		t.Fatalf("state = %s, want idle", s)
	}
}

func TestWaitSettled_ReturnsImmediatelyWhenIdle(t *testing.T) {
	rt := newTestRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !rt.WaitSettled(ctx) {
		t.Fatal("WaitSettled = false for an already-idle session")
	}
}

func TestWaitQuiescent_WaitsForAutonomousBackgroundWork(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{Agent: fas.fakeAgent, Subscriber: &fas.fakeSubscriber})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// These are the three independent sources of follow-up work that may outlive
	// a foreground RunEnded: auto-verify, goal verification, and an async child.
	rt.sctx.beginAutoVerify()
	rt.sctx.beginGoalVerify()
	rt.Bus.Publish(SubagentStarted{JobID: "child"})

	go func() {
		time.Sleep(20 * time.Millisecond)
		rt.sctx.endAutoVerify()
		time.Sleep(20 * time.Millisecond)
		rt.sctx.endGoalVerify()
		time.Sleep(20 * time.Millisecond)
		rt.Bus.Publish(SubagentEnded{JobID: "child", Status: "completed"})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	if !rt.WaitQuiescent(ctx) {
		t.Fatal("WaitQuiescent = false, want true")
	}
	if elapsed := time.Since(started); elapsed < 45*time.Millisecond {
		t.Fatalf("WaitQuiescent returned after %v, before background work ended", elapsed)
	}
}

func TestBashBackgroundWorkSettlesAfterReinjection(t *testing.T) {
	fas := newFakeAgentSubscriber()
	rt, err := NewSessionRuntime(RuntimeConfig{Agent: fas.fakeAgent, Subscriber: &fas.fakeSubscriber})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	rt.Bus.Publish(BashJobStarted{JobID: "bash-1"})
	rt.Bus.Drain(time.Second)
	if !rt.sctx.hasBackgroundWork() {
		t.Fatal("background bash was not tracked")
	}

	// The tray is finalized first, but this must not let headless quiescence
	// win before the completion notification has been scheduled.
	rt.Bus.Publish(BashJobEnded{JobID: "bash-1", Status: "completed"})
	rt.Bus.Drain(time.Second)
	if !rt.sctx.hasBackgroundWork() {
		t.Fatal("BashJobEnded cleared background work before reinjection")
	}

	rt.Bus.Publish(BashCompleted{JobID: "bash-1", Text: "done"})
	rt.Bus.Drain(time.Second)
	if !rt.sctx.hasBackgroundWork() {
		t.Fatal("BashCompleted cleared background work before delivery settled")
	}

	rt.Bus.Publish(BashJobSettled{JobID: "bash-1"})
	rt.Bus.Drain(time.Second)
	if rt.sctx.hasBackgroundWork() {
		t.Fatal("BashJobSettled did not clear background work")
	}
}

func TestWaitSettled_TimesOutWhileRunning(t *testing.T) {
	rt := newTestRuntime(t)
	if err := rt.State.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if rt.WaitSettled(ctx) {
		t.Fatal("WaitSettled = true, want false (run never settled)")
	}
	if s := rt.State.Current(); s != StateRunning {
		t.Fatalf("state = %s, want running", s)
	}
}

// fileSessionPersister is a rebinding persister backed by real session JSON.
// It lets the switching regression test verify disk state, not merely a fake
// Snapshot call's arguments.

// A run reaches StateIdle before it publishes RunEnded. WaitSettled must not
// return in that window: on shutdown it is the caller's signal that every
// subscriber has seen the turn, and returning early is how a finished turn
// gets torn down before anything could react to it (the owner-report loss).
func TestWaitSettled_WaitsForTheTerminalEventNotJustIdleState(t *testing.T) {
	rt := newTestRuntime(t)
	sctx := rt.Context()

	// Reproduce the gap exactly as launchRun creates it: a generation is
	// reserved (which writes the start anchor), the state goes back to idle,
	// and RunEnded has not been published yet.
	sctx.runMu.Lock()
	_, gen := sctx.newRunContext()
	sctx.runMu.Unlock()
	if err := rt.State.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}
	if err := rt.State.Transition(StateIdle); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if rt.WaitSettled(ctx) {
		t.Fatal("WaitSettled = true inside the idle-before-RunEnded gap; the turn was not terminal yet")
	}

	// Once the run settles the way the launch path settles it — after the
	// terminal event — the waiter is released.
	settled := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		settled <- rt.WaitSettled(ctx)
	}()
	time.Sleep(20 * time.Millisecond)
	sctx.Bus.Publish(RunEnded{RunGen: gen})
	sctx.settleRun(gen)

	select {
	case ok := <-settled:
		if !ok {
			t.Fatal("WaitSettled = false after the run settled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitSettled did not wake when the run settled")
	}
}

// The panic path settles too: a run that blows up must not strand a waiter
// (shutdown would then spend its whole budget on a run that is long over).
func TestWaitSettled_ReleasedByThePanicPath(t *testing.T) {
	rt := newTestRuntime(t)
	sctx := rt.Context()
	launchRun(sctx, "panics", func(context.Context) ([]core.AgentMessage, error) {
		panic("boom")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !rt.WaitSettled(ctx) {
		t.Fatal("WaitSettled = false after a panicking run; its anchor was never cleared")
	}
}

// Generations can overlap: a queued steer's run is admitted while the previous
// one is still finishing. The UI start anchor deliberately tracks only the
// newest, so a terminal barrier built on it would report "settled" while an
// older generation had yet to publish its outcome. WaitSettled must wait for
// every admitted generation.
func TestWaitSettled_WaitsForEveryAdmittedGeneration(t *testing.T) {
	rt := newTestRuntime(t)
	sctx := rt.Context()

	sctx.runMu.Lock()
	_, gen1 := sctx.newRunContext()
	_, gen2 := sctx.newRunContext() // gen2 overwrites the UI anchor
	sctx.runMu.Unlock()
	if gen1 == gen2 {
		t.Fatal("generations must be distinct")
	}

	// The older generation finishes first.
	sctx.Bus.Publish(RunEnded{RunGen: gen1})
	sctx.settleRun(gen1)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if rt.WaitSettled(ctx) {
		t.Fatal("WaitSettled = true while a second admitted generation had not published RunEnded")
	}

	settled := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		settled <- rt.WaitSettled(ctx)
	}()
	time.Sleep(20 * time.Millisecond)
	sctx.Bus.Publish(RunEnded{RunGen: gen2})
	sctx.settleRun(gen2)

	select {
	case ok := <-settled:
		if !ok {
			t.Fatal("WaitSettled = false once every generation had settled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitSettled did not wake when the last generation settled")
	}
}

// The newest generation clearing the UI anchor must not release the barrier
// for an older one that is still running: the two are separate on purpose.
func TestWaitSettled_NewerGenerationDoesNotReleaseAnOlderOne(t *testing.T) {
	rt := newTestRuntime(t)
	sctx := rt.Context()
	sctx.runMu.Lock()
	_, gen1 := sctx.newRunContext()
	_, gen2 := sctx.newRunContext()
	sctx.runMu.Unlock()

	// gen2 settles; gen1 is still in flight.
	sctx.Bus.Publish(RunEnded{RunGen: gen2})
	sctx.settleRun(gen2)
	if !sctx.runInFlight() {
		t.Fatal("the older generation was released by the newer one settling")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if rt.WaitSettled(ctx) {
		t.Fatal("WaitSettled = true while the older generation was still in flight")
	}
	sctx.Bus.Publish(RunEnded{RunGen: gen1})
	sctx.settleRun(gen1)
	if sctx.runInFlight() {
		t.Fatal("the session is still in flight after every generation settled")
	}
}

// A close is admitted through DoIfQuiescent. The state reaches idle before the
// terminal event is published, so admitting a close there would tear the
// runtime down with the outcome still unseen by its subscribers — the report
// loss, reached through close instead of shutdown.
func TestDoIfQuiescent_RefusesInsideTheTerminalGap(t *testing.T) {
	rt := newTestRuntime(t)
	sctx := rt.Context()

	sctx.runMu.Lock()
	_, gen := sctx.newRunContext()
	sctx.runMu.Unlock()
	// The run has gone back to idle but has not published RunEnded yet.
	if err := rt.State.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}
	if err := rt.State.Transition(StateIdle); err != nil {
		t.Fatal(err)
	}

	if rt.DoIfQuiescent(func() {}) {
		t.Fatal("DoIfQuiescent admitted a close while a terminal event was outstanding")
	}

	sctx.Bus.Publish(RunEnded{RunGen: gen})
	sctx.settleRun(gen)
	if !rt.DoIfQuiescent(func() {}) {
		t.Fatal("DoIfQuiescent refused a genuinely quiescent session")
	}
}

// Close admission is a distinct operation: it atomically closes run admission
// while State is idle, so a producer that reaches reserveRunSlot afterwards
// cannot launch into the runtime being torn down. A failed close reopens that
// admission and leaves the runtime usable.
func TestAdmitCloseIfQuiescent_ClosesAndReopensRunAdmission(t *testing.T) {
	rt := newTestRuntime(t)
	sctx := rt.Context()

	called := false
	if !rt.AdmitCloseIfQuiescent(func() { called = true }) {
		t.Fatal("close admission refused an idle runtime")
	}
	if !called {
		t.Fatal("close admission did not run its callback")
	}
	if err := reserveRunSlot(sctx); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("reserveRunSlot after close admission = %v, want ErrSessionBusy", err)
	}
	if got := rt.State.Current(); got != StateIdle {
		t.Fatalf("state after refused run = %s, want idle", got)
	}

	rt.ReopenRunAdmission()
	if err := reserveRunSlot(sctx); err != nil {
		t.Fatalf("reserveRunSlot after reopening admission: %v", err)
	}
}

// The 15s an owner report allows for background work has to be 15s in total.
// Each internal drain is capped by what is left of the deadline, so a caller
// cannot be made to wait its budget plus however many drains were in flight.
//
// A deliberately slow subscriber is what makes this discriminating: with no
// events in flight a drain returns at once and any cap looks correct.
func TestWaitQuiescent_RespectsTheCallersDeadline(t *testing.T) {
	rt := newTestRuntime(t)
	// Background work that never ends: only the deadline can end this wait.
	rt.Context().trackBackgroundEvent(BashJobStarted{JobID: "bash-endless"})

	var slow sync.WaitGroup
	slow.Add(1)
	unsub := rt.Bus.SubscribeAll(func(any) { time.Sleep(80 * time.Millisecond) })
	defer unsub()
	go func() {
		defer slow.Done()
		for i := 0; i < 40; i++ { // ~3.2s of subscriber work to drain
			rt.Bus.Publish(BashJobOutput{JobID: "bash-endless"})
		}
	}()

	const budget = 300 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	start := time.Now()
	if rt.WaitQuiescent(ctx) {
		t.Fatal("WaitQuiescent = true while a background job was still running")
	}
	elapsed := time.Since(start)
	slow.Wait()
	// Generous slack for a loaded machine, but far below budget plus the two
	// uncapped 2s drains the old code would have sat through.
	if elapsed > budget+time.Second {
		t.Fatalf("WaitQuiescent took %v for a %v budget; the drains overran the deadline", elapsed, budget)
	}
}

// An already-expired context must not buy even one drain.
func TestWaitQuiescent_ExpiredContextReturnsAtOnce(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Context().trackBackgroundEvent(BashJobStarted{JobID: "bash-endless"})

	unsub := rt.Bus.SubscribeAll(func(any) { time.Sleep(80 * time.Millisecond) })
	defer unsub()
	var pub sync.WaitGroup
	pub.Add(1)
	go func() {
		defer pub.Done()
		for i := 0; i < 40; i++ {
			rt.Bus.Publish(BashJobOutput{JobID: "bash-endless"})
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if rt.WaitQuiescent(ctx) {
		t.Fatal("WaitQuiescent = true with background work outstanding")
	}
	elapsed := time.Since(start)
	pub.Wait()
	if elapsed > time.Second {
		t.Fatalf("an expired context still cost %v in drains", elapsed)
	}
}

func TestDrainBudget(t *testing.T) {
	if got := drainBudget(context.Background()); got != maxQuiescenceDrain {
		t.Fatalf("no deadline should allow the full drain, got %v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if got := drainBudget(ctx); got <= 0 || got > 50*time.Millisecond {
		t.Fatalf("a short deadline must shorten the drain, got %v", got)
	}
	expired, cancelExpired := context.WithCancel(context.Background())
	cancelExpired()
	deadlined, cancelDeadlined := context.WithTimeout(context.Background(), -time.Second)
	defer cancelDeadlined()
	if got := drainBudget(deadlined); got != 0 {
		t.Fatalf("an expired deadline must allow no drain, got %v", got)
	}
	_ = expired
}
