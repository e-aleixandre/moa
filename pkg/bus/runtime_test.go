package bus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
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
