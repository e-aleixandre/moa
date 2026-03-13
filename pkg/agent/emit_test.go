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

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if received.Load() >= 2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected 2 events, got %d", received.Load())
}

func TestEmitter_UnsubscribeRemoves(t *testing.T) {
	e := NewEmitter(nil)
	var received atomic.Int32

	unsub := e.Subscribe(func(evt core.AgentEvent) {
		received.Add(1)
	})

	// Deliver one event and poll until received.
	e.Emit(core.AgentEvent{Type: core.AgentEventStart})
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if received.Load() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if received.Load() != 1 {
		t.Fatalf("expected 1 event before unsub, got %d", received.Load())
	}

	// Unsubscribe, then emit another event.
	unsub()
	e.Emit(core.AgentEvent{Type: core.AgentEventEnd})

	// Drain to ensure the emitter processed the second event.
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
