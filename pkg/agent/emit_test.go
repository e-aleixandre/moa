package agent

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestEmitter_BasicDelivery(t *testing.T) {
	e := NewEmitter(nil)
	var received atomic.Int32

	e.Subscribe(func(evt core.AgentEvent) {
		received.Add(1)
	})

	e.Emit(core.AgentEvent{Type: core.AgentEventStart})
	e.Emit(core.AgentEvent{Type: core.AgentEventEnd})

	e.Drain(time.Second)

	if got := received.Load(); got != 2 {
		t.Fatalf("expected 2 events, got %d", got)
	}
}

func TestEmitter_UnsubscribeRemoves(t *testing.T) {
	e := NewEmitter(nil)
	var received atomic.Int32

	unsub := e.Subscribe(func(evt core.AgentEvent) {
		received.Add(1)
	})

	// Deliver one event and wait for it.
	e.Emit(core.AgentEvent{Type: core.AgentEventStart})
	e.Drain(time.Second)
	if received.Load() != 1 {
		t.Fatalf("expected 1 event before unsub, got %d", received.Load())
	}

	// Unsubscribe, then emit another event.
	unsub()
	e.Emit(core.AgentEvent{Type: core.AgentEventEnd})

	// Short drain — the event should not reach the removed subscriber.
	e.Drain(200 * time.Millisecond)
	if received.Load() != 1 {
		t.Fatalf("expected still 1 event after unsub, got %d", received.Load())
	}
}

func TestEmitter_DoubleUnsubscribe(t *testing.T) {
	e := NewEmitter(nil)
	unsub := e.Subscribe(func(evt core.AgentEvent) {})
	unsub()
	unsub() // Should not panic
}

func TestEmitter_DrainDeliversAllEvents(t *testing.T) {
	e := NewEmitter(nil)
	const n = 50
	var received atomic.Int32

	e.Subscribe(func(evt core.AgentEvent) {
		received.Add(1)
	})

	for i := 0; i < n; i++ {
		e.Emit(core.AgentEvent{Type: core.AgentEventStart})
	}

	e.Drain(time.Second)

	if got := received.Load(); got != int32(n) {
		t.Fatalf("expected %d events after drain, got %d", n, got)
	}
}

func TestEmitter_DrainEmptyImmediate(t *testing.T) {
	e := NewEmitter(nil)
	e.Subscribe(func(evt core.AgentEvent) {})

	start := time.Now()
	e.Drain(5 * time.Second)
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Fatalf("drain with empty buffer took %v, expected near-instant", elapsed)
	}
}

func TestEmitter_DrainTimeout(t *testing.T) {
	e := NewEmitter(nil)
	// Subscriber that blocks forever
	e.Subscribe(func(evt core.AgentEvent) {
		select {} // block
	})

	// Fill with more events than buffer to ensure channel stays non-empty
	for i := 0; i < 300; i++ {
		e.Emit(core.AgentEvent{Type: core.AgentEventStart})
	}

	start := time.Now()
	e.Drain(50 * time.Millisecond)
	elapsed := time.Since(start)

	if elapsed < 40*time.Millisecond {
		t.Fatalf("drain returned too early: %v", elapsed)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("drain took too long: %v", elapsed)
	}
}

func TestEmitter_ConcurrentEmitUnsubscribe(t *testing.T) {
	e := NewEmitter(nil)
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		unsub := e.Subscribe(func(evt core.AgentEvent) {})
		emitDone := make(chan struct{})

		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				e.Emit(core.AgentEvent{Type: core.AgentEventStart})
			}
			close(emitDone)
		}()
		go func() {
			defer wg.Done()
			// Wait until at least some emits have happened, then unsubscribe.
			<-emitDone
			unsub()
		}()
	}

	wg.Wait() // Should not race or panic
}

// --- New tests for deterministic drain ---

func TestEmitter_DrainWaitsForHandler(t *testing.T) {
	e := NewEmitter(nil)
	gate := make(chan struct{})
	var processed atomic.Bool

	e.Subscribe(func(evt core.AgentEvent) {
		<-gate // block until gate opens
		processed.Store(true)
	})

	e.Emit(core.AgentEvent{Type: core.AgentEventStart})

	// Open gate after 200ms
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(gate)
	}()

	start := time.Now()
	e.Drain(5 * time.Second)
	elapsed := time.Since(start)

	if !processed.Load() {
		t.Fatal("handler should have completed before Drain returned")
	}
	if elapsed < 150*time.Millisecond {
		t.Fatalf("Drain returned too early (%v), handler should have blocked it", elapsed)
	}
}

func TestEmitter_DrainAfterPanic(t *testing.T) {
	e := NewEmitter(nil)
	var count atomic.Int32

	e.Subscribe(func(evt core.AgentEvent) {
		count.Add(1)
		if count.Load() == 1 {
			panic("boom")
		}
	})

	// First event panics, second should still be processed
	e.Emit(core.AgentEvent{Type: core.AgentEventStart})
	e.Emit(core.AgentEvent{Type: core.AgentEventEnd})

	start := time.Now()
	e.Drain(time.Second)
	elapsed := time.Since(start)

	if elapsed > 200*time.Millisecond {
		t.Fatalf("Drain after panic took too long: %v (possible deadlock)", elapsed)
	}
	if got := count.Load(); got != 2 {
		t.Fatalf("expected 2 events processed (one with panic), got %d", got)
	}
}

func TestEmitter_DroppedEventNotCounted(t *testing.T) {
	e := NewEmitter(nil)
	started := make(chan struct{}, 1)
	gate := make(chan struct{})
	var processed atomic.Int32

	e.Subscribe(func(evt core.AgentEvent) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-gate // block until gate opens
		processed.Add(1)
	})

	// Send first event and wait for handler to start processing it.
	// This ensures the handler holds 1 event while we fill the buffer.
	e.Emit(core.AgentEvent{Type: core.AgentEventStart})
	<-started

	// Fill buffer completely (256 events in channel)
	for i := 0; i < subscriberBuffer; i++ {
		e.Emit(core.AgentEvent{Type: core.AgentEventStart})
	}

	// This one gets dropped — buffer is full, handler is blocked
	e.Emit(core.AgentEvent{Type: core.AgentEventStart})

	// Unblock handler
	close(gate)

	start := time.Now()
	e.Drain(5 * time.Second)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("Drain waited too long: %v (likely counting dropped event)", elapsed)
	}
	// 1 in handler + 256 in buffer = 257 accepted; 1 dropped
	if got := processed.Load(); got != int32(subscriberBuffer+1) {
		t.Fatalf("expected %d events processed (1 dropped), got %d", subscriberBuffer+1, got)
	}
}

func TestEmitter_UnsubscribeWithQueuedEvents(t *testing.T) {
	e := NewEmitter(nil)
	gate := make(chan struct{})
	var processed atomic.Int32

	unsub := e.Subscribe(func(evt core.AgentEvent) {
		<-gate
		processed.Add(1)
	})

	// Queue 10 events while handler is blocked
	for i := 0; i < 10; i++ {
		e.Emit(core.AgentEvent{Type: core.AgentEventStart})
	}

	// Unblock handler and immediately unsubscribe.
	// The unsubscribe drain loop should process remaining events.
	close(gate)
	unsub()

	// Drain should return quickly — subscriber is gone and drained its queue.
	start := time.Now()
	e.Drain(time.Second)
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Fatalf("Drain after unsubscribe took too long: %v", elapsed)
	}
}

func TestEmitter_ConcurrentEmitDrain(t *testing.T) {
	e := NewEmitter(nil)
	var totalProcessed atomic.Int64

	e.Subscribe(func(evt core.AgentEvent) {
		totalProcessed.Add(1)
	})

	var wg sync.WaitGroup

	// 5 goroutines emitting 100 events each
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				e.Emit(core.AgentEvent{Type: core.AgentEventStart})
			}
		}()
	}

	// Concurrent drain calls
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.Drain(2 * time.Second)
		}()
	}

	wg.Wait()

	// Final drain to ensure everything is settled
	e.Drain(2 * time.Second)

	// Under concurrent pressure, some events may be dropped (buffer full).
	// The key properties: no panics, no races, and all accepted events are processed.
	got := totalProcessed.Load()
	if got < 1 || got > 500 {
		t.Fatalf("unexpected event count: %d (expected 1..500)", got)
	}
}
