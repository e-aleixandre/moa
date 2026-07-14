package bus

import (
	"sync"
	"testing"
	"time"
)

// The QueueCommand handler enqueues the command as a barrier item and announces
// it with a CommandQueued event so frontends can render an optimistic chip.
func TestHandler_QueueCommand_EnqueuesBarrierAndAnnounces(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	// A barrier is queued while a run is in flight; occupy the state so the
	// pump (kicked after enqueue to close the idle orphan race) abstains and
	// leaves the barrier pending for the running agent's next idle point.
	if err := sctx.State.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}

	var (
		mu     sync.Mutex
		queued []CommandQueued
	)
	b.Subscribe(func(e CommandQueued) {
		mu.Lock()
		queued = append(queued, e)
		mu.Unlock()
	})

	if err := b.Execute(QueueCommand{Raw: "/compact"}); err != nil {
		t.Fatalf("QueueCommand: %v", err)
	}
	b.Drain(time.Second)

	// The agent queue holds a barrier item carrying the raw command.
	pending := fa.PendingSteers()
	if len(pending) != 1 {
		t.Fatalf("expected 1 queued item, got %d", len(pending))
	}
	if !pending[0].IsBarrier() || pending[0].Command != "/compact" {
		t.Fatalf("expected barrier /compact, got %+v", pending[0])
	}
	if pending[0].ID == "" {
		t.Fatal("barrier must have a minted ID")
	}

	// A CommandQueued event was published with the same ID and raw line.
	mu.Lock()
	defer mu.Unlock()
	if len(queued) != 1 {
		t.Fatalf("expected 1 CommandQueued event, got %d", len(queued))
	}
	if queued[0].Raw != "/compact" || queued[0].ID != pending[0].ID {
		t.Fatalf("CommandQueued mismatch: %+v vs item %+v", queued[0], pending[0])
	}
}

// A caller-supplied ID is preserved (not overwritten) so an optimistic chip on
// the client keeps a stable identity end-to-end.
func TestHandler_QueueCommand_PreservesCallerID(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	if err := sctx.State.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}

	if err := b.Execute(QueueCommand{ID: "cmd-1", Raw: "/model sonnet"}); err != nil {
		t.Fatalf("QueueCommand: %v", err)
	}
	pending := fa.PendingSteers()
	if len(pending) != 1 || pending[0].ID != "cmd-1" {
		t.Fatalf("expected caller ID cmd-1, got %+v", pending)
	}
}

// When the queue is full the handler surfaces ErrSteerQueueFull and does not
// announce a chip.
func TestHandler_QueueCommand_FullQueue(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{steerFull: true}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	var (
		amu       sync.Mutex
		announced bool
	)
	b.Subscribe(func(e CommandQueued) { amu.Lock(); announced = true; amu.Unlock() })

	err := b.Execute(QueueCommand{Raw: "/compact"})
	if err != ErrSteerQueueFull {
		t.Fatalf("expected ErrSteerQueueFull, got %v", err)
	}
	b.Drain(time.Second)
	amu.Lock()
	defer amu.Unlock()
	if announced {
		t.Fatal("must not announce CommandQueued when the queue is full")
	}
}
