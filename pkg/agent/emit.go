package agent

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/ealeixandre/moa/pkg/core"
)

const subscriberBuffer = 256

// Emitter fans out events to subscribers asynchronously.
// Each subscriber has a buffered channel. If the buffer fills, events are dropped.
// Panics in handlers are recovered.
//
// Events are delivered asynchronously. There is no guarantee that all events
// are delivered by the time the agent loop returns.
type Emitter struct {
	subs   []*subscriber
	logger *slog.Logger
	mu     sync.RWMutex
}

type subscriber struct {
	ch     chan core.AgentEvent
	fn     func(core.AgentEvent)
	done   chan struct{}
	closed atomic.Bool // atomic flag for concurrent safety
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
// Events are delivered asynchronously; there is no guarantee events are delivered
// before Run() returns.
func (e *Emitter) Subscribe(fn func(core.AgentEvent)) func() {
	sub := &subscriber{
		ch:   make(chan core.AgentEvent, subscriberBuffer),
		fn:   fn,
		done: make(chan struct{}),
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

// Emit sends an event to all active subscribers. Non-blocking.
// Skips subscribers that are closed or have full buffers.
func (e *Emitter) Emit(event core.AgentEvent) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, sub := range e.subs {
		if sub.closed.Load() {
			continue
		}
		select {
		case sub.ch <- event:
		default:
			e.logger.Warn("subscriber buffer full, dropping event", "type", event.Type)
		}
	}
}

func (s *subscriber) loop(logger *slog.Logger) {
	for {
		select {
		case event, ok := <-s.ch:
			if !ok {
				return
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						logger.Error("subscriber panic", "event", event.Type, "error", r)
					}
				}()
				s.fn(event)
			}()
		case <-s.done:
			return
		}
	}
}
