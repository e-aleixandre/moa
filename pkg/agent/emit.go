package agent

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

const subscriberBuffer = 256

// Emitter fans out events to subscribers asynchronously.
// Each subscriber has a buffered channel. If the buffer fills, events are dropped.
// Panics in handlers are recovered without stranding the inflight counter.
//
// Drain() waits until all accepted in-flight events have been processed.
// Dropped events (full buffer) are not tracked and won't delay Drain.
type Emitter struct {
	subs   []*subscriber
	logger *slog.Logger
	mu     sync.RWMutex
}

type subscriber struct {
	ch       chan core.AgentEvent
	fn       func(core.AgentEvent)
	done     chan struct{}
	closed   atomic.Bool
	inflight atomic.Int64  // events in channel + being processed
	idleCh   chan struct{} // buffered(1); receives signal when inflight hits 0
}

// NewEmitter creates an emitter.
func NewEmitter(logger *slog.Logger) *Emitter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Emitter{logger: logger}
}

// Subscribe registers a listener. Returns an unsubscribe function (safe to call multiple times).
// The listener runs in its own goroutine and receives events asynchronously.
func (e *Emitter) Subscribe(fn func(core.AgentEvent)) func() {
	sub := &subscriber{
		ch:     make(chan core.AgentEvent, subscriberBuffer),
		fn:     fn,
		done:   make(chan struct{}),
		idleCh: make(chan struct{}, 1),
	}
	go sub.loop(e.logger)

	e.mu.Lock()
	e.subs = append(e.subs, sub)
	e.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			sub.closed.Store(true)
			close(sub.done)
			// Remove from subs slice
			e.mu.Lock()
			for i, s := range e.subs {
				if s == sub {
					e.subs = append(e.subs[:i], e.subs[i+1:]...)
					break
				}
			}
			e.mu.Unlock()
		})
	}
}

// isLossyAgentEvent reports whether an event may be dropped under backpressure.
// Only high-frequency streaming deltas are lossy; every structural event
// (message_end, tool_execution_end, agent_end, …) MUST be delivered — dropping
// one can strand the UI (e.g. a tool badge stuck on "running"). Mirrors the
// bus's isLossyEvent.
func isLossyAgentEvent(event core.AgentEvent) bool {
	switch event.Type {
	case core.AgentEventMessageUpdate, core.AgentEventToolExecUpdate:
		return true
	default:
		return false
	}
}

// Emit sends an event to all active subscribers. Lossy delta events are
// dropped when a subscriber's buffer is full; structural events block until
// there is room (or the subscriber is torn down) so they are never lost.
func (e *Emitter) Emit(event core.AgentEvent) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	lossy := isLossyAgentEvent(event)
	for _, sub := range e.subs {
		if sub.closed.Load() {
			continue
		}
		sub.inflight.Add(1)
		if lossy {
			select {
			case sub.ch <- event:
				// counted and enqueued
			default:
				// buffer full — rollback count, delta dropped
				if sub.inflight.Add(-1) == 0 {
					sub.signalIdle()
				}
				e.logger.Warn("subscriber buffer full, dropping delta", "type", event.Type)
			}
			continue
		}
		// Structural event: block until enqueued, or bail if the subscriber
		// is torn down (done is closed before the unsub takes e.mu, so this
		// never deadlocks against unsubscribe).
		select {
		case sub.ch <- event:
			// counted and enqueued
		case <-sub.done:
			if sub.inflight.Add(-1) == 0 {
				sub.signalIdle()
			}
		}
	}
}

// Drain waits until all in-flight events have been processed by all subscribers,
// or timeout expires. The timeout is a safety net for stuck handlers.
func (e *Emitter) Drain(timeout time.Duration) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	e.mu.RLock()
	subs := make([]*subscriber, len(e.subs))
	copy(subs, e.subs)
	e.mu.RUnlock()

	for _, sub := range subs {
		for sub.inflight.Load() > 0 {
			select {
			case <-sub.idleCh:
				// Re-signal for concurrent Drain calls on the same subscriber.
				// Without this, a competing Drain could miss the idle transition.
				if sub.inflight.Load() == 0 {
					sub.signalIdle()
				}
			case <-deadline.C:
				e.logger.Warn("drain timeout waiting for subscriber",
					"inflight", sub.inflight.Load())
				return
			}
		}
	}
}

func (s *subscriber) loop(logger *slog.Logger) {
	for {
		select {
		case event := <-s.ch:
			s.processEvent(event, logger)
		case <-s.done:
			// Drain remaining counted events before exiting.
			// After closed=true, no new events will be enqueued,
			// so this loop terminates.
			for {
				select {
				case event := <-s.ch:
					s.processEvent(event, logger)
				default:
					return
				}
			}
		}
	}
}

func (s *subscriber) processEvent(event core.AgentEvent, logger *slog.Logger) {
	defer func() {
		if s.inflight.Add(-1) == 0 {
			s.signalIdle()
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			logger.Error("subscriber panic", "event", event.Type, "error", r)
		}
	}()
	s.fn(event)
}

// signalIdle sends a non-blocking signal on idleCh to wake up Drain.
func (s *subscriber) signalIdle() {
	select {
	case s.idleCh <- struct{}{}:
	default:
	}
}
