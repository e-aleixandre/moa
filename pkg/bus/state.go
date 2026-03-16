package bus

import (
	"fmt"
	"sync"
)

// SessionState represents the state of a session.
type SessionState string

const (
	StateIdle       SessionState = "idle"
	StateRunning    SessionState = "running"
	StatePermission SessionState = "permission"
	StateError      SessionState = "error"
)

// validTransitions defines which state transitions are legal.
var validTransitions = map[SessionState]map[SessionState]bool{
	StateIdle:       {StateRunning: true},
	StateRunning:    {StateIdle: true, StateError: true, StatePermission: true},
	StatePermission: {StateRunning: true, StateIdle: true},
	StateError:      {StateRunning: true, StateIdle: true},
}

// StateMachine manages session state with validated transitions.
// Thread-safe. Publishes StateChanged events on every transition.
type StateMachine struct {
	mu      sync.Mutex
	current SessionState
	bus     EventBus
	sid     string
}

// NewStateMachine creates a new state machine starting in StateIdle.
func NewStateMachine(bus EventBus, sessionID string) *StateMachine {
	return &StateMachine{current: StateIdle, bus: bus, sid: sessionID}
}

// Current returns the current state.
func (sm *StateMachine) Current() SessionState {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.current
}

// Transition moves to a new state. Returns error if the transition is invalid.
// Publishes StateChanged on success.
func (sm *StateMachine) Transition(to SessionState) error {
	return sm.TransitionWithError(to, "")
}

// TransitionWithError moves to a new state with an optional error message.
// Returns error if the transition is invalid. Publishes StateChanged on success.
func (sm *StateMachine) TransitionWithError(to SessionState, errMsg string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if allowed := validTransitions[sm.current]; !allowed[to] {
		return fmt.Errorf("invalid state transition: %s → %s", sm.current, to)
	}
	sm.current = to
	sm.bus.Publish(StateChanged{
		SessionID: sm.sid,
		State:     string(to),
		Error:     errMsg,
	})
	return nil
}

// MustTransition panics on invalid transitions. Use in code paths where
// the transition is guaranteed valid by construction.
func (sm *StateMachine) MustTransition(to SessionState) {
	if err := sm.Transition(to); err != nil {
		panic(err)
	}
}

// ForceState sets state without validation or events. For session restore only.
func (sm *StateMachine) ForceState(s SessionState) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.current = s
}
