package bus

import (
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
