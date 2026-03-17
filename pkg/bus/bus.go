package bus

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

// ErrNoHandler is returned by Execute/Query when no handler is registered for the type.
var ErrNoHandler = errors.New("bus: no handler registered for this type")

// ErrClosed is returned by Execute/Query when the bus has been closed.
var ErrClosed = errors.New("bus: closed")

// ---------------------------------------------------------------------------
// Interface
// ---------------------------------------------------------------------------

// EventBus mediates typed events, commands, and queries between components.
//
// Events are async (fan-out to subscribers via buffered channels).
// Commands and queries are synchronous (one handler per type).
//
// Top-level event/command/query payloads must be non-nil value structs.
// Nested fields may contain pointers, slices, and maps — subscribers must
// treat all payloads as read-only (no mutation after publish).
type EventBus interface {
	// Publish fans out an event to all subscribers of that type.
	// No-op after Close. Panics on nil event.
	Publish(event any)

	// Subscribe registers a handler for events of a specific type.
	// handler must be func(T) where T is a concrete struct (not pointer).
	// Returns an unsubscribe function (idempotent, non-blocking, safe to call
	// from within the handler itself).
	// Returns a no-op unsubscribe if bus is already closed.
	// Panics on invalid signature or pointer type.
	Subscribe(handler any) func()

	// SubscribeAll registers a handler that receives ALL events regardless of type.
	// The handler receives events in publication order within a single goroutine,
	// guaranteeing ordering. Events are delivered to SubscribeAll handlers BEFORE
	// typed subscribers.
	// Returns an unsubscribe function (idempotent, non-blocking).
	// Returns a no-op unsubscribe if bus is already closed.
	SubscribeAll(handler func(any)) func()

	// Execute dispatches a command to its registered handler synchronously.
	// Returns ErrNoHandler if none registered, ErrClosed if bus is closed.
	// Recovers handler panics and returns them as wrapped errors.
	// Panics on nil command.
	Execute(command any) error

	// Query dispatches a query to its registered handler synchronously.
	// Returns (nil, ErrNoHandler) if none registered, (nil, ErrClosed) if closed.
	// Recovers handler panics and returns them as wrapped errors.
	// Panics on nil query.
	Query(query any) (any, error)

	// OnCommand registers a handler for a specific command type.
	// handler must be func(T) error where T is a concrete struct (not pointer).
	// Panics on invalid signature, pointer type, or duplicate registration.
	OnCommand(handler any)

	// OnQuery registers a handler for a specific query type.
	// handler must be func(T) (R, error) where T is a concrete struct (not pointer).
	// Panics on invalid signature, pointer type, or duplicate registration.
	OnQuery(handler any)

	// Drain waits for all in-flight event handlers to finish, or until timeout.
	Drain(timeout time.Duration)

	// Close marks the bus as closed. Idempotent.
	// New Publish calls become no-ops; Execute/Query return ErrClosed.
	// Subscriber goroutines drain remaining queued events and exit.
	Close()
}

// ---------------------------------------------------------------------------
// QueryTyped — generic helper
// ---------------------------------------------------------------------------

// QueryTyped is a type-safe wrapper around Query that avoids manual type assertions.
//
//	msgs, err := bus.QueryTyped[GetMessages, []core.AgentMessage](b, GetMessages{})
func QueryTyped[Q any, R any](b EventBus, q Q) (R, error) {
	result, err := b.Query(q)
	if err != nil {
		var zero R
		return zero, err
	}
	typed, ok := result.(R)
	if !ok {
		var zero R
		return zero, fmt.Errorf("bus: query result type mismatch: got %T, want %T", result, zero)
	}
	return typed, nil
}

// ---------------------------------------------------------------------------
// LocalBus — in-process implementation
// ---------------------------------------------------------------------------

const subscriberBuffer = 256

// LocalBus is an in-process EventBus implementation.
// Create with NewLocalBus; zero value is NOT usable.
type LocalBus struct {
	closed atomic.Bool

	mu        sync.RWMutex
	eventSubs map[reflect.Type][]*subscriber
	allSubs   []*subscriber // SubscribeAll handlers — receive ALL events in order
	cmdH      map[reflect.Type]reflect.Value
	queryH    map[reflect.Type]reflect.Value

	// Global inflight counter for Drain.
	inflight atomic.Int64
	idleCh   chan struct{} // buffered(1), signalled when inflight reaches 0
}

// NewLocalBus creates a ready-to-use LocalBus.
func NewLocalBus() *LocalBus {
	return &LocalBus{
		eventSubs: make(map[reflect.Type][]*subscriber),
		cmdH:      make(map[reflect.Type]reflect.Value),
		queryH:    make(map[reflect.Type]reflect.Value),
		idleCh:    make(chan struct{}, 1),
	}
}

// subscriber is an async event handler with its own goroutine and buffer.
type subscriber struct {
	ch       chan any
	fn       reflect.Value
	done     chan struct{}   // closed to signal drain-and-exit
	exited   chan struct{}   // closed when goroutine returns
	stopOnce sync.Once      // guards close(done) — safe for concurrent close/unsub
	stopped  atomic.Bool    // fast check: true after stop() called
	bus      *LocalBus      // back-reference for inflight tracking
	isAll    bool           // true for SubscribeAll handlers (fn is func(any))
}

// stop signals the subscriber goroutine to drain and exit. Safe to call
// concurrently from both Close() and unsubscribe — only the first call
// actually closes the done channel.
func (s *subscriber) stop() {
	s.stopOnce.Do(func() {
		s.stopped.Store(true)
		close(s.done)
	})
}

// ---------------------------------------------------------------------------
// Subscribe / Publish
// ---------------------------------------------------------------------------

// Subscribe implements EventBus.
func (b *LocalBus) Subscribe(handler any) func() {
	ht := reflect.TypeOf(handler)
	if ht == nil || ht.Kind() != reflect.Func {
		panic("bus: Subscribe handler must be a function")
	}
	if ht.NumIn() != 1 {
		panic(fmt.Sprintf("bus: Subscribe handler must have exactly 1 parameter, got %d", ht.NumIn()))
	}
	if ht.NumOut() != 0 {
		panic(fmt.Sprintf("bus: Subscribe handler must have no return values, got %d", ht.NumOut()))
	}
	eventType := ht.In(0)
	if eventType.Kind() == reflect.Ptr {
		panic("bus: handler parameter must be a struct, not a pointer")
	}
	if eventType.Kind() != reflect.Struct {
		panic(fmt.Sprintf("bus: handler parameter must be a struct, got %s", eventType.Kind()))
	}

	// Check closed under write lock to prevent race with Close().
	b.mu.Lock()
	if b.closed.Load() {
		b.mu.Unlock()
		return func() {} // no-op unsubscribe
	}

	sub := &subscriber{
		ch:     make(chan any, subscriberBuffer),
		fn:     reflect.ValueOf(handler),
		done:   make(chan struct{}),
		exited: make(chan struct{}),
		bus:    b,
	}
	go sub.loop()
	b.eventSubs[eventType] = append(b.eventSubs[eventType], sub)
	b.mu.Unlock()

	var unsubOnce sync.Once
	return func() {
		unsubOnce.Do(func() {
			// Remove from map under write lock.
			b.mu.Lock()
			subs := b.eventSubs[eventType]
			for i, s := range subs {
				if s == sub {
					b.eventSubs[eventType] = append(subs[:i], subs[i+1:]...)
					break
				}
			}
			b.mu.Unlock()

			// Signal stop (safe even if Close already called stop).
			sub.stop()
			// Do NOT block on <-sub.exited — this may be called from the
			// handler's own goroutine, which would deadlock.
		})
	}
}

// SubscribeAll implements EventBus.
func (b *LocalBus) SubscribeAll(handler func(any)) func() {
	if handler == nil {
		panic("bus: SubscribeAll handler must not be nil")
	}

	b.mu.Lock()
	if b.closed.Load() {
		b.mu.Unlock()
		return func() {} // no-op unsubscribe
	}

	sub := &subscriber{
		ch:     make(chan any, subscriberBuffer),
		fn:     reflect.ValueOf(handler),
		done:   make(chan struct{}),
		exited: make(chan struct{}),
		bus:    b,
		isAll:  true,
	}
	go sub.loop()
	b.allSubs = append(b.allSubs, sub)
	b.mu.Unlock()

	var unsubOnce sync.Once
	return func() {
		unsubOnce.Do(func() {
			b.mu.Lock()
			for i, s := range b.allSubs {
				if s == sub {
					b.allSubs = append(b.allSubs[:i], b.allSubs[i+1:]...)
					break
				}
			}
			b.mu.Unlock()
			sub.stop()
		})
	}
}

// Publish implements EventBus.
func (b *LocalBus) Publish(event any) {
	if event == nil {
		panic("bus: nil event")
	}
	if b.closed.Load() {
		return
	}
	et := reflect.TypeOf(event)

	// Hold read lock during the entire enqueue loop. Non-blocking sends make
	// this cheap. This ensures we never send to a subscriber whose goroutine
	// has already exited (unsubscribe/close take write lock before stopping).
	b.mu.RLock()

	// SubscribeAll handlers first — guarantees they see events before typed subs.
	for _, sub := range b.allSubs {
		if sub.stopped.Load() {
			continue
		}
		b.inflight.Add(1)
		select {
		case sub.ch <- event:
		default:
			b.decrementInflight()
		}
	}

	// Typed subscribers.
	subs := b.eventSubs[et]
	for _, sub := range subs {
		if sub.stopped.Load() {
			continue // skip subscribers in the process of shutting down
		}
		b.inflight.Add(1)
		select {
		case sub.ch <- event:
			// enqueued
		default:
			// buffer full — drop event for this subscriber to avoid deadlock
			b.decrementInflight()
		}
	}
	b.mu.RUnlock()
}

// ---------------------------------------------------------------------------
// OnCommand / Execute
// ---------------------------------------------------------------------------

// OnCommand implements EventBus.
func (b *LocalBus) OnCommand(handler any) {
	ht := reflect.TypeOf(handler)
	if ht == nil || ht.Kind() != reflect.Func {
		panic("bus: OnCommand handler must be a function")
	}
	if ht.NumIn() != 1 {
		panic(fmt.Sprintf("bus: OnCommand handler must have exactly 1 parameter, got %d", ht.NumIn()))
	}
	if ht.NumOut() != 1 {
		panic(fmt.Sprintf("bus: OnCommand handler must return exactly 1 value (error), got %d", ht.NumOut()))
	}
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	if !ht.Out(0).Implements(errorType) {
		panic(fmt.Sprintf("bus: OnCommand handler must return error, got %s", ht.Out(0)))
	}
	cmdType := ht.In(0)
	if cmdType.Kind() == reflect.Ptr {
		panic("bus: command parameter must be a struct, not a pointer")
	}
	if cmdType.Kind() != reflect.Struct {
		panic(fmt.Sprintf("bus: command parameter must be a struct, got %s", cmdType.Kind()))
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.cmdH[cmdType]; exists {
		panic(fmt.Sprintf("bus: duplicate command handler for %s", cmdType))
	}
	b.cmdH[cmdType] = reflect.ValueOf(handler)
}

// Execute implements EventBus.
func (b *LocalBus) Execute(command any) (retErr error) {
	if command == nil {
		panic("bus: nil command")
	}
	if b.closed.Load() {
		return ErrClosed
	}
	ct := reflect.TypeOf(command)
	b.mu.RLock()
	h, ok := b.cmdH[ct]
	b.mu.RUnlock()
	if !ok {
		return ErrNoHandler
	}
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("bus: command handler panic: %v", r)
		}
	}()
	result := h.Call([]reflect.Value{reflect.ValueOf(command)})
	if errIface := result[0].Interface(); errIface != nil {
		return errIface.(error)
	}
	return nil
}

// ---------------------------------------------------------------------------
// OnQuery / Query
// ---------------------------------------------------------------------------

// OnQuery implements EventBus.
func (b *LocalBus) OnQuery(handler any) {
	ht := reflect.TypeOf(handler)
	if ht == nil || ht.Kind() != reflect.Func {
		panic("bus: OnQuery handler must be a function")
	}
	if ht.NumIn() != 1 {
		panic(fmt.Sprintf("bus: OnQuery handler must have exactly 1 parameter, got %d", ht.NumIn()))
	}
	if ht.NumOut() != 2 {
		panic(fmt.Sprintf("bus: OnQuery handler must return exactly 2 values (R, error), got %d", ht.NumOut()))
	}
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	if !ht.Out(1).Implements(errorType) {
		panic(fmt.Sprintf("bus: OnQuery handler second return must be error, got %s", ht.Out(1)))
	}
	queryType := ht.In(0)
	if queryType.Kind() == reflect.Ptr {
		panic("bus: query parameter must be a struct, not a pointer")
	}
	if queryType.Kind() != reflect.Struct {
		panic(fmt.Sprintf("bus: query parameter must be a struct, got %s", queryType.Kind()))
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.queryH[queryType]; exists {
		panic(fmt.Sprintf("bus: duplicate query handler for %s", queryType))
	}
	b.queryH[queryType] = reflect.ValueOf(handler)
}

// Query implements EventBus.
func (b *LocalBus) Query(query any) (retResult any, retErr error) {
	if query == nil {
		panic("bus: nil query")
	}
	if b.closed.Load() {
		return nil, ErrClosed
	}
	qt := reflect.TypeOf(query)
	b.mu.RLock()
	h, ok := b.queryH[qt]
	b.mu.RUnlock()
	if !ok {
		return nil, ErrNoHandler
	}
	defer func() {
		if r := recover(); r != nil {
			retResult = nil
			retErr = fmt.Errorf("bus: query handler panic: %v", r)
		}
	}()
	result := h.Call([]reflect.Value{reflect.ValueOf(query)})
	val := result[0].Interface()
	if errIface := result[1].Interface(); errIface != nil {
		return val, errIface.(error)
	}
	return val, nil
}

// ---------------------------------------------------------------------------
// Drain / Close
// ---------------------------------------------------------------------------

// Drain implements EventBus.
func (b *LocalBus) Drain(timeout time.Duration) {
	if b.inflight.Load() == 0 {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-b.idleCh:
			if b.inflight.Load() == 0 {
				return
			}
		case <-timer.C:
			return
		}
	}
}

// Close implements EventBus.
func (b *LocalBus) Close() {
	if !b.closed.CompareAndSwap(false, true) {
		return // idempotent
	}

	// Take write lock so no Publish can be in its enqueue loop.
	b.mu.Lock()
	allSubs := make([]*subscriber, 0, len(b.allSubs))
	allSubs = append(allSubs, b.allSubs...)
	for _, subs := range b.eventSubs {
		allSubs = append(allSubs, subs...)
	}
	b.allSubs = nil
	b.eventSubs = make(map[reflect.Type][]*subscriber)
	b.mu.Unlock()

	// Stop all subscribers (safe even if unsubscribe already called stop).
	for _, sub := range allSubs {
		sub.stop()
	}

	// Wait for all goroutines with a hard timeout.
	deadline := time.After(5 * time.Second)
	for _, sub := range allSubs {
		select {
		case <-sub.exited:
		case <-deadline:
			return
		}
	}
}

// ---------------------------------------------------------------------------
// subscriber internals
// ---------------------------------------------------------------------------

func (s *subscriber) loop() {
	defer close(s.exited)
	for {
		select {
		case event := <-s.ch:
			s.process(event)
		case <-s.done:
			// Drain remaining events in the channel.
			for {
				select {
				case event := <-s.ch:
					s.process(event)
				default:
					return
				}
			}
		}
	}
}

func (s *subscriber) process(event any) {
	defer s.bus.decrementInflight()
	defer func() { recover() }() // swallow handler panics
	if s.isAll {
		// SubscribeAll handler: fn is func(any), call directly for efficiency.
		s.fn.Interface().(func(any))(event)
	} else {
		s.fn.Call([]reflect.Value{reflect.ValueOf(event)})
	}
}

func (b *LocalBus) decrementInflight() {
	if b.inflight.Add(-1) == 0 {
		// Signal idle — non-blocking send.
		select {
		case b.idleCh <- struct{}{}:
		default:
		}
	}
}
