package agent

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ealeixandre/go-agent/pkg/core"
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

	// Deliver one event
	e.Emit(core.AgentEvent{Type: core.AgentEventStart})
	time.Sleep(50 * time.Millisecond)
	if received.Load() != 1 {
		t.Fatalf("expected 1 event before unsub, got %d", received.Load())
	}

	// Unsubscribe
	unsub()

	// This event should NOT be delivered
	e.Emit(core.AgentEvent{Type: core.AgentEventEnd})
	time.Sleep(50 * time.Millisecond)
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

func TestEmitter_ConcurrentEmitUnsubscribe(t *testing.T) {
	e := NewEmitter(nil)
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		unsub := e.Subscribe(func(evt core.AgentEvent) {})

		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				e.Emit(core.AgentEvent{Type: core.AgentEventStart})
			}
		}()
		go func() {
			defer wg.Done()
			time.Sleep(time.Millisecond)
			unsub()
		}()
	}

	wg.Wait() // Should not race or panic
}
