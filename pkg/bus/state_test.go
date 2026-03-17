package bus

import (
	"sync"
	"testing"
	"time"
)

func TestStateMachine_ValidTransitions(t *testing.T) {
	tests := []struct {
		from SessionState
		to   SessionState
	}{
		{StateIdle, StateRunning},
		{StateRunning, StateIdle},
		{StateRunning, StateError},
		{StateRunning, StatePermission},
		{StatePermission, StateRunning},
		{StatePermission, StateIdle},
		{StateError, StateRunning},
		{StateError, StateIdle},
	}

	for _, tt := range tests {
		t.Run(string(tt.from)+"→"+string(tt.to), func(t *testing.T) {
			b := NewLocalBus()
			defer b.Close()
			sm := NewStateMachine(b, "s1")
			sm.ForceState(tt.from)

			if err := sm.Transition(tt.to); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sm.Current() != tt.to {
				t.Fatalf("state = %q, want %q", sm.Current(), tt.to)
			}
		})
	}
}

func TestStateMachine_InvalidTransitions(t *testing.T) {
	tests := []struct {
		from SessionState
		to   SessionState
	}{
		{StateIdle, StatePermission},
		{StateIdle, StateError},
		{StateIdle, StateIdle},
		{StatePermission, StateError},
		{StatePermission, StatePermission},
		{StateRunning, StateRunning},
		{StateError, StateError},
		{StateError, StatePermission},
	}

	for _, tt := range tests {
		t.Run(string(tt.from)+"→"+string(tt.to), func(t *testing.T) {
			b := NewLocalBus()
			defer b.Close()
			sm := NewStateMachine(b, "s1")
			sm.ForceState(tt.from)

			if err := sm.Transition(tt.to); err == nil {
				t.Fatalf("expected error for %s → %s", tt.from, tt.to)
			}
			// State should not change.
			if sm.Current() != tt.from {
				t.Fatalf("state changed to %q after failed transition", sm.Current())
			}
		})
	}
}

func TestStateMachine_PublishesStateChanged(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sm := NewStateMachine(b, "sess-1")

	got := make(chan StateChanged, 1)
	b.Subscribe(func(e StateChanged) { got <- e })

	if err := sm.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}

	b.Drain(time.Second)
	select {
	case e := <-got:
		if e.SessionID != "sess-1" {
			t.Fatalf("SessionID = %q", e.SessionID)
		}
		if e.State != "running" {
			t.Fatalf("State = %q", e.State)
		}
		if e.Error != "" {
			t.Fatalf("Error = %q", e.Error)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for StateChanged")
	}
}

func TestStateMachine_TransitionWithError(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sm := NewStateMachine(b, "s1")
	sm.ForceState(StateRunning)

	got := make(chan StateChanged, 1)
	b.Subscribe(func(e StateChanged) { got <- e })

	if err := sm.TransitionWithError(StateError, "provider timeout"); err != nil {
		t.Fatal(err)
	}

	b.Drain(time.Second)
	select {
	case e := <-got:
		if e.State != "error" {
			t.Fatalf("State = %q", e.State)
		}
		if e.Error != "provider timeout" {
			t.Fatalf("Error = %q", e.Error)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestStateMachine_ForceState(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sm := NewStateMachine(b, "s1")

	got := make(chan StateChanged, 1)
	b.Subscribe(func(e StateChanged) { got <- e })

	// ForceState should not publish events.
	sm.ForceState(StateError)
	if sm.Current() != StateError {
		t.Fatalf("state = %q, want error", sm.Current())
	}

	b.Drain(100 * time.Millisecond)
	select {
	case e := <-got:
		t.Fatalf("unexpected StateChanged: %+v", e)
	default:
		// good — no event
	}
}

func TestStateMachine_ConcurrentAccess(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sm := NewStateMachine(b, "s1")

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Some transitions will fail — that's expected.
			_ = sm.Transition(StateRunning)
			_ = sm.Transition(StateIdle)
			_ = sm.Current()
		}()
	}
	wg.Wait()
	// No race detector failures = pass.
}

func TestStateMachine_MustTransition_Panics(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sm := NewStateMachine(b, "s1")

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()

	sm.MustTransition(StateError) // idle → error is invalid
}

func TestStateMachine_InitialState(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sm := NewStateMachine(b, "s1")

	if sm.Current() != StateIdle {
		t.Fatalf("initial state = %q, want idle", sm.Current())
	}
}

func TestStateMachine_LastError(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	sm := NewStateMachine(b, "s1")

	// Initially empty.
	if sm.LastError() != "" {
		t.Fatalf("initial lastError = %q, want empty", sm.LastError())
	}

	// Transition to running (no error).
	_ = sm.Transition(StateRunning)
	if sm.LastError() != "" {
		t.Fatalf("after running, lastError = %q", sm.LastError())
	}

	// Transition to error with message.
	_ = sm.TransitionWithError(StateError, "provider timeout")
	if sm.LastError() != "provider timeout" {
		t.Fatalf("lastError = %q, want 'provider timeout'", sm.LastError())
	}

	// Transition to idle clears error.
	_ = sm.Transition(StateIdle)
	if sm.LastError() != "" {
		t.Fatalf("after idle, lastError = %q, want empty", sm.LastError())
	}
}
