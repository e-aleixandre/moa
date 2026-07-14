package bus

import (
	"sync"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// collect subscribes and records events of type T until the test ends.
func collect[T any](b EventBus) (*[]T, *sync.Mutex) {
	var (
		mu   sync.Mutex
		list []T
	)
	b.Subscribe(func(e T) {
		mu.Lock()
		list = append(list, e)
		mu.Unlock()
	})
	return &list, &mu
}

// waitForLen polls until the guarded slice reaches at least n entries or the
// deadline passes. The pump runs on its own goroutine, so tests can't rely on a
// single bus Drain; they wait on the observable outcome instead.
func waitForLen[T any](b EventBus, list *[]T, mu *sync.Mutex, n int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		b.Drain(50 * time.Millisecond)
		mu.Lock()
		got := len(*list)
		mu.Unlock()
		if got >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// A queued /compact barrier is executed by the pump at the idle point and the
// pump announces CommandDequeued{Executed:true}.
func TestPump_QueuedBarrierExecutesAtIdle(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	deq, dmu := collect[CommandDequeued](b)
	compactStarted, cmu := collect[CompactionStarted](b)

	// Enqueue a /compact barrier (as QueueCommand would).
	fa.Steer(core.SteerItem{ID: "c1", Text: "/compact", Command: "/compact"})

	// Drive an idle signal (as a finished run would).
	requestPump(sctx)
	if !waitForLen(b, deq, dmu, 1, 2*time.Second) {
		t.Fatal("barrier never dequeued")
	}

	dmu.Lock()
	defer dmu.Unlock()
	if len(*deq) != 1 || (*deq)[0].ID != "c1" || !(*deq)[0].Executed {
		t.Fatalf("expected 1 executed CommandDequeued for c1, got %+v", *deq)
	}
	cmu.Lock()
	defer cmu.Unlock()
	if len(*compactStarted) != 1 {
		t.Fatalf("expected the barrier to run compact once, got %d CompactionStarted", len(*compactStarted))
	}
	// Barrier consumed.
	if len(fa.PendingSteers()) != 0 {
		t.Fatalf("barrier still queued: %+v", fa.PendingSteers())
	}
}

// A QueueCommand that lands on an already-idle session (the classifier saw the
// session busy, but the run finished and drained an empty queue before the
// barrier arrived) must still be executed: the handler kicks the pump after
// enqueuing, so the barrier is not stranded. Regression for the orphan-barrier
// race.
func TestHandler_QueueCommand_IdleSelfDrains(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	// State is idle: no external requestPump is issued; only the handler's own
	// post-enqueue kick can drain the barrier.

	deq, dmu := collect[CommandDequeued](b)
	if err := b.Execute(QueueCommand{ID: "c1", Raw: "/compact"}); err != nil {
		t.Fatalf("QueueCommand: %v", err)
	}
	if !waitForLen(b, deq, dmu, 1, 2*time.Second) {
		t.Fatal("idle barrier never executed — orphaned")
	}
	dmu.Lock()
	defer dmu.Unlock()
	if (*deq)[0].ID != "c1" || !(*deq)[0].Executed {
		t.Fatalf("expected executed barrier c1, got %+v", *deq)
	}
	if len(fa.PendingSteers()) != 0 {
		t.Fatalf("barrier still queued: %+v", fa.PendingSteers())
	}
}

// Strict send order: message → /compact → message. The steer before the barrier
// stays queued for the current run (not tested here), the barrier runs at idle,
// and the trailing steer starts a fresh run via SendItems.
func TestPump_BarrierThenTrailingSteer(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	// Queue: barrier /compact, then a trailing steer.
	fa.Steer(core.SteerItem{ID: "c1", Text: "/compact", Command: "/compact"})
	fa.Steer(core.SteerItem{ID: "s2", Text: "after compact"})

	requestPump(sctx)
	b.Drain(2 * time.Second)

	// Poll until the trailing steer was delivered as a fresh run.
	deadline := time.After(2 * time.Second)
	for {
		fa.mu.Lock()
		n := len(fa.sentItems)
		fa.mu.Unlock()
		if n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("trailing steer never delivered after barrier")
		case <-time.After(5 * time.Millisecond):
		}
	}
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if fa.sentItems[0].ID != "s2" {
		t.Fatalf("expected trailing steer s2 delivered, got %+v", fa.sentItems)
	}
}

// A barrier is only executed when the session is idle: if a run is in flight the
// pump defers, then runs once the session returns to idle.
func TestPump_DefersWhileRunning(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)
	// Move to running.
	if err := sctx.State.Transition(StateRunning); err != nil {
		t.Fatal(err)
	}

	deq, dmu := collect[CommandDequeued](b)
	fa.Steer(core.SteerItem{ID: "c1", Text: "/compact", Command: "/compact"})

	requestPump(sctx)
	b.Drain(time.Second)

	dmu.Lock()
	if len(*deq) != 0 {
		dmu.Unlock()
		t.Fatalf("barrier ran while session busy: %+v", *deq)
	}
	dmu.Unlock()
	if len(fa.PendingSteers()) != 1 {
		t.Fatalf("barrier should still be queued while busy, got %+v", fa.PendingSteers())
	}

	// Return to idle and pump again.
	if err := sctx.State.Transition(StateIdle); err != nil {
		t.Fatal(err)
	}
	requestPump(sctx)
	if !waitForLen(b, deq, dmu, 1, 2*time.Second) {
		t.Fatal("barrier should run once idle")
	}

	dmu.Lock()
	defer dmu.Unlock()
	if len(*deq) != 1 {
		t.Fatalf("barrier should run once idle, got %+v", *deq)
	}
}

// Concurrent idle signals must not spawn overlapping pumps that double-execute a
// barrier. popBarrier makes execution single-winner; this drives many pumps
// concurrently and asserts exactly one execution.
func TestPump_CoalescesConcurrentSignals(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	deq, dmu := collect[CommandDequeued](b)
	fa.Steer(core.SteerItem{ID: "c1", Text: "/clear", Command: "/clear"})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			requestPump(sctx)
		}()
	}
	wg.Wait()
	if !waitForLen(b, deq, dmu, 1, 2*time.Second) {
		t.Fatal("barrier never executed")
	}
	// Give any erroneously-spawned second pump a chance to double-execute.
	b.Drain(200 * time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	b.Drain(200 * time.Millisecond)

	dmu.Lock()
	defer dmu.Unlock()
	if len(*deq) != 1 {
		t.Fatalf("expected exactly one barrier execution, got %d: %+v", len(*deq), *deq)
	}
}

// An empty queue is a no-op: the pump returns without side effects.
func TestPump_EmptyQueueNoop(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	deq, dmu := collect[CommandDequeued](b)
	requestPump(sctx)
	b.Drain(time.Second)

	dmu.Lock()
	defer dmu.Unlock()
	if len(*deq) != 0 {
		t.Fatalf("empty queue produced events: %+v", *deq)
	}
}

// INV-2 producer gate: a user SendPrompt that arrives while the queue is
// non-empty must NOT start a run (which would jump ahead of the queued item);
// it is converted into a steer at the tail of the queue.
func TestPump_SendPromptGatedWhenQueueNonEmpty(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	// A barrier is already queued (e.g. /compact issued mid-run).
	fa.Steer(core.SteerItem{ID: "c1", Text: "/compact", Command: "/compact"})

	if err := b.Execute(SendPrompt{Text: "later message"}); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	b.Drain(time.Second)

	// The prompt must not have started a run.
	if fa.wasSendCalled() {
		t.Fatal("SendPrompt started a run despite a non-empty queue (jumped the barrier)")
	}
	// It was appended to the queue behind the barrier.
	pending := fa.PendingSteers()
	if len(pending) != 2 || pending[0].ID != "c1" || pending[1].Text != "later message" {
		t.Fatalf("expected [barrier c1, steer 'later message'], got %+v", pending)
	}
	if pending[1].IsBarrier() {
		t.Fatal("the gated prompt must be a steer, not a barrier")
	}
}

// An internal prompt (goal relaunch / auto-verify) is exempt from the gate: it
// starts its run even with items queued, since it is the machinery the queue is
// waiting on.
func TestPump_InternalPromptExemptFromGate(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	fa := &fakeAgent{}
	sctx := newTestSessionContextWithState(b, fa)
	RegisterHandlers(sctx)

	fa.Steer(core.SteerItem{ID: "c1", Text: "/compact", Command: "/compact"})

	if err := b.Execute(SendPrompt{Text: "goal turn", Custom: map[string]any{"source": "goal"}}); err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !fa.wasSendCalled() {
		time.Sleep(5 * time.Millisecond)
	}
	if !fa.wasSendCalled() {
		t.Fatal("internal (goal) prompt should be exempt from the queue gate and start its run")
	}
}

// Stress the strict-order window: with a barrier queued and repeated idle
// signals racing concurrent user SendPrompts, no user prompt may ever start a
// run ahead of the barrier. Any prompt that arrives while the queue is
// non-empty must land behind it (INV-2), and the pump is the only consumer.
func TestPump_StrictOrderUnderRace(t *testing.T) {
	for iter := 0; iter < 50; iter++ {
		b := NewLocalBus()
		fa := &fakeAgent{}
		sctx := newTestSessionContextWithState(b, fa)
		RegisterHandlers(sctx)

		// Block the compact so the barrier occupies the session while the racing
		// SendPrompt tries to sneak in.
		compactGate := make(chan struct{})
		fa.compactHook = func() { <-compactGate }

		fa.Steer(core.SteerItem{ID: "c1", Text: "/compact", Command: "/compact"})

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); requestPump(sctx) }()
		go func() { defer wg.Done(); _ = b.Execute(SendPrompt{Text: "racer"}) }()

		// Let the race resolve, then release the compact.
		time.Sleep(2 * time.Millisecond)
		close(compactGate)
		wg.Wait()
		b.Drain(2 * time.Second)

		// The racer must never have started a run before the barrier: since the
		// barrier held the session, SendPrompt either saw the non-empty queue
		// (gated → steer) or a busy state (transition failed). Either way, Send
		// must not have been invoked with "racer" ahead of the compact.
		if fa.getSendPrompt() == "racer" && !fa.wasCompactCalled() {
			b.Close()
			t.Fatalf("iter %d: racer ran before the barrier compact", iter)
		}
		b.Close()
	}
}
