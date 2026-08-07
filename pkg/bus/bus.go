package bus

import (
	"context"
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

	// SubscribeAllSeq is the sequenced counterpart of SubscribeAll. Sequence
	// numbers are monotonically increasing within this bus and identify a
	// publication boundary; gaps are valid when consumers drop lossy events.
	SubscribeAllSeq(handler func(seq uint64, event any)) func()

	// SubscribeAttentionSeq registers the bounded, sequenced attention tracker.
	// Its event selection and compact projection are declarative and fixed: no
	// caller code runs in Publish's critical section. Overflowed reports a
	// dropped occurrence so callers must explicitly resynchronize before
	// accepting another acknowledgement boundary.
	SubscribeAttentionSeq(handler func(seq uint64, event AttentionSequenceEvent)) SeqFenceSubscription

	// LastSeq returns the most recently accepted publication sequence.
	LastSeq() uint64

	// CaptureSeq returns the most recently accepted publication sequence while
	// sequence allocation is serialized with Publish. It is an exact
	// publication cut.
	CaptureSeq() uint64

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

// SeqFenceSubscription is a filtered sequence subscription with a causal
// completion fence. Fence returns false when the bus or subscription has
// already stopped; otherwise its channel closes after all earlier matching
// publications have run the handler.
type SeqFenceSubscription interface {
	Unsubscribe()
	// CaptureCut returns the bus sequence cut together with the overflow and
	// clear versions at that exact publication boundary. A snapshot must use
	// this instead of reading overflow after taking its sequence cut: another
	// snapshot may otherwise clear an overflow contained by this one.
	CaptureCut() (seq, overflow, cleared uint64, ok bool)
	// CaptureCutBefore is CaptureCut bounded by deadline. On expiry it returns
	// a best-effort sequence cut and ok=false; callers must treat the matching
	// attention boundary as unbound rather than use its watermarks.
	CaptureCutBefore(deadline time.Time) (seq, overflow, cleared uint64, ok bool)
	// Fence returns a completion channel and the overflow version observed at
	// the marker's publication cut. Clearing can only use that version: an
	// overflow after the marker remains visible to the next boundary attempt.
	Fence() (<-chan struct{}, uint64, bool)
	// FenceBefore is Fence with a deadline for placing its publication marker.
	// On expiry it returns ok=false; implementations must not wait past
	// deadline to acquire their publication boundary.
	FenceBefore(deadline time.Time) (<-chan struct{}, uint64, bool)
	Done() <-chan struct{}
	Overflowed() bool
	OverflowVersion() uint64
	ClearOverflowThrough(version uint64)
	// ClearOverflowThroughContext is the cancellable form for deferred
	// acknowledgement work. It returns false when the context, bus, or
	// subscription has stopped before the watermark was cleared.
	ClearOverflowThroughContext(ctx context.Context, version uint64) bool
	// ClearOverflowThroughBefore clears an observed overflow watermark before
	// deadline. false leaves it pending so the caller can arrange an eventual
	// clear without extending a latency-sensitive operation.
	ClearOverflowThroughBefore(version uint64, deadline time.Time) bool
}

// AttentionSequenceKind identifies the compact attention occurrence retained
// by SubscribeAttentionSeq.
type AttentionSequenceKind uint8

const (
	AttentionRunEnded AttentionSequenceKind = iota
	AttentionPermissionRequested
	AttentionAskUserRequested
	AttentionStateError
)

// AttentionSequenceEvent is the fixed compact projection used by the
// correctness tracker. In particular it never retains RunEnded.FinalText.
type AttentionSequenceEvent struct {
	Kind      AttentionSequenceKind
	Cancelled bool
	Errored   bool
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

// subscriberBuffer bounds how many lossy (streaming-delta) events may queue
// per subscriber before new ones are dropped. Lossless (structural) events are
// never dropped and are not subject to this cap.
const subscriberBuffer = 256

// correctnessSubscriberBuffer bounds a rare structural subscription without
// making ordinary event delivery lossy. Overflow is surfaced to its owner,
// which must explicitly resynchronize before accepting another boundary.
const correctnessSubscriberBuffer = 64

// LocalBus is an in-process EventBus implementation.
// Create with NewLocalBus; zero value is NOT usable.
type LocalBus struct {
	closed atomic.Bool

	mu         sync.RWMutex
	publishMu  sync.Mutex // serializes sequence allocation and enqueue order
	eventSubs  map[reflect.Type][]*subscriber
	allSubs    []*subscriber // SubscribeAll handlers — receive ALL events in order
	allSeqSubs []*subscriber
	cmdH       map[reflect.Type]reflect.Value
	queryH     map[reflect.Type]reflect.Value

	// Global inflight counter for Drain.
	inflight atomic.Int64
	idleCh   chan struct{} // buffered(1), signalled when inflight reaches 0
	seq      atomic.Uint64
	// published distinguishes a new bus's initial sequence zero from the
	// otherwise ambiguous value after uint64 sequence wrap.
	published atomic.Bool
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

// subscriber is an async event handler with its own goroutine and queue.
// Events are delivered through an unbounded FIFO queue: lossless events are
// always enqueued, while lossy (delta) events are dropped once the queue depth
// reaches subscriberBuffer. A buffered notify channel wakes the goroutine.
// This lets a slow subscriber (e.g. a remote WS client) fall behind on deltas
// without losing structural events and without applying backpressure to the
// publisher.
type subscriber struct {
	mu     sync.Mutex
	queue  []queuedEvent // FIFO of pending events
	notify chan struct{} // buffered(1): signals the queue is non-empty

	fn              reflect.Value
	done            chan struct{} // closed to signal drain-and-exit
	exited          chan struct{} // closed when goroutine returns
	stopOnce        sync.Once     // guards close(done) — safe for concurrent close/unsub
	stopped         atomic.Bool   // fast check: true after stop() called
	bus             *LocalBus     // back-reference for inflight tracking
	isAll           bool          // true for SubscribeAll handlers (fn is func(any))
	isSeqAll        bool          // true for SubscribeAllSeq (fn is func(uint64, any))
	isAttentionSeq  bool          // true for SubscribeAttentionSeq
	queueCap        int
	discardOnStop   bool
	overflow        atomic.Uint64
	clearedOverflow atomic.Uint64
}

type queuedEvent struct {
	seq   uint64
	event any
	fence chan struct{}
}

// stop signals the subscriber goroutine to drain and exit. Safe to call
// concurrently from both Close() and unsubscribe — only the first call
// actually closes the done channel.
func (s *subscriber) stop() {
	s.stopOnce.Do(func() {
		s.stopped.Store(true)
		if s.discardOnStop {
			// Discard the backlog instead of draining it, so a large queue is
			// reclaimed promptly. A queued fence is dropped WITHOUT closing it:
			// closing would falsely claim every earlier matching publication had
			// run. Waiters observe Done() instead and treat the bound as unknown.
			s.mu.Lock()
			queued := s.queue
			s.queue = nil
			s.mu.Unlock()
			for _, event := range queued {
				if event.fence != nil {
					continue
				}
				s.bus.decrementInflight()
			}
		}
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
		notify: make(chan struct{}, 1),
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
		notify: make(chan struct{}, 1),
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

// SubscribeAllSeq implements EventBus.SubscribeAllSeq.
func (b *LocalBus) SubscribeAllSeq(handler func(uint64, any)) func() {
	if handler == nil {
		panic("bus: SubscribeAllSeq handler must not be nil")
	}
	b.mu.Lock()
	if b.closed.Load() {
		b.mu.Unlock()
		return func() {}
	}
	sub := &subscriber{
		notify: make(chan struct{}, 1), fn: reflect.ValueOf(handler),
		done: make(chan struct{}), exited: make(chan struct{}), bus: b, isSeqAll: true,
	}
	go sub.loop()
	b.allSeqSubs = append(b.allSeqSubs, sub)
	b.mu.Unlock()
	var unsubOnce sync.Once
	return func() {
		unsubOnce.Do(func() {
			b.mu.Lock()
			for i, s := range b.allSeqSubs {
				if s == sub {
					b.allSeqSubs = append(b.allSeqSubs[:i], b.allSeqSubs[i+1:]...)
					break
				}
			}
			b.mu.Unlock()
			sub.stop()
		})
	}
}

// SubscribeAttentionSeq implements EventBus.SubscribeAttentionSeq.
func (b *LocalBus) SubscribeAttentionSeq(handler func(uint64, AttentionSequenceEvent)) SeqFenceSubscription {
	if handler == nil {
		panic("bus: SubscribeAttentionSeq handler must not be nil")
	}
	b.mu.Lock()
	if b.closed.Load() {
		b.mu.Unlock()
		return stoppedSeqFenceSubscription{}
	}
	sub := &subscriber{
		notify: make(chan struct{}, 1), fn: reflect.ValueOf(handler),
		done: make(chan struct{}), exited: make(chan struct{}), bus: b,
		isAttentionSeq: true,
		queueCap:       correctnessSubscriberBuffer, discardOnStop: true,
	}
	go sub.loop()
	b.allSeqSubs = append(b.allSeqSubs, sub)
	b.mu.Unlock()
	return &seqFenceSubscription{bus: b, sub: sub}
}

type seqFenceSubscription struct {
	bus  *LocalBus
	sub  *subscriber
	once sync.Once
}

// initialCutCapturer is deliberately narrower than SeqFenceSubscription: it
// lets snapshots that use LocalBus distinguish the initial zero sequence from
// a wrapped zero without making alternate subscription implementations claim
// that they can do so.
type initialCutCapturer interface {
	CaptureCutWithInitial() (seq, overflow, cleared uint64, initial, ok bool)
	CaptureCutWithInitialBefore(deadline time.Time) (seq, overflow, cleared uint64, initial, ok bool)
}

func (s *seqFenceSubscription) Unsubscribe() {
	s.once.Do(func() {
		s.bus.mu.Lock()
		for i, candidate := range s.bus.allSeqSubs {
			if candidate == s.sub {
				s.bus.allSeqSubs = append(s.bus.allSeqSubs[:i], s.bus.allSeqSubs[i+1:]...)
				break
			}
		}
		s.bus.mu.Unlock()
		s.sub.stop()
	})
}

func (s *seqFenceSubscription) CaptureCut() (uint64, uint64, uint64, bool) {
	// publishMu puts the sequence and both overflow watermarks in one total
	// order with publication and overflow recording. ClearOverflowThrough also
	// takes this lock, so a concurrent init cannot move the clear watermark
	// between this snapshot's cut and its overflow observation.
	s.bus.publishMu.Lock()
	defer s.bus.publishMu.Unlock()
	seq, overflow, cleared, _, ok := s.captureCutLocked()
	return seq, overflow, cleared, ok
}

func (s *seqFenceSubscription) CaptureCutWithInitial() (uint64, uint64, uint64, bool, bool) {
	s.bus.publishMu.Lock()
	defer s.bus.publishMu.Unlock()
	return s.captureCutLocked()
}

// CaptureCutBefore is the deadline-aware form used during WebSocket init. If
// publication is stalled, its sequence is still sufficient to replay events
// after the stream snapshot (whose caller holds streamMu), but its overflow
// watermarks are deliberately unusable: the init is explicitly unbound.
func (s *seqFenceSubscription) CaptureCutBefore(deadline time.Time) (uint64, uint64, uint64, bool) {
	if !tryLockBefore(&s.bus.publishMu, deadline) {
		return s.bus.seq.Load(), 0, 0, false
	}
	defer s.bus.publishMu.Unlock()
	if !time.Now().Before(deadline) {
		return s.bus.seq.Load(), 0, 0, false
	}
	seq, overflow, cleared, _, ok := s.captureCutLocked()
	return seq, overflow, cleared, ok
}

func (s *seqFenceSubscription) CaptureCutWithInitialBefore(deadline time.Time) (uint64, uint64, uint64, bool, bool) {
	if !tryLockBefore(&s.bus.publishMu, deadline) {
		return s.bus.seq.Load(), 0, 0, false, false
	}
	defer s.bus.publishMu.Unlock()
	if !time.Now().Before(deadline) {
		return s.bus.seq.Load(), 0, 0, false, false
	}
	return s.captureCutLocked()
}

func (s *seqFenceSubscription) captureCutLocked() (uint64, uint64, uint64, bool, bool) {
	if s.bus.closed.Load() || s.sub.stopped.Load() {
		return 0, 0, 0, false, false
	}
	seq := s.bus.seq.Load()
	return seq, s.sub.overflow.Load(), s.sub.clearedOverflow.Load(), seq == 0 && !s.bus.published.Load(), true
}

func (s *seqFenceSubscription) Fence() (<-chan struct{}, uint64, bool) {
	// publishMu makes this marker a publication cut. A matching event cannot
	// be inserted before the marker after this point.
	s.bus.publishMu.Lock()
	defer s.bus.publishMu.Unlock()
	return s.fenceLocked()
}

// FenceBefore is the deadline-aware form used by latency-sensitive snapshot
// initialization. It includes waiting to acquire the publication cut lock in
// the deadline instead of letting a contended publisher extend the fence cap.
func (s *seqFenceSubscription) FenceBefore(deadline time.Time) (<-chan struct{}, uint64, bool) {
	if !tryLockBefore(&s.bus.publishMu, deadline) {
		return nil, 0, false
	}
	defer s.bus.publishMu.Unlock()
	if !time.Now().Before(deadline) {
		return nil, 0, false
	}
	return s.fenceLocked()
}

func (s *seqFenceSubscription) fenceLocked() (<-chan struct{}, uint64, bool) {
	if s.bus.closed.Load() || s.sub.stopped.Load() {
		return nil, 0, false
	}
	s.bus.mu.RLock()
	if s.sub.stopped.Load() {
		s.bus.mu.RUnlock()
		return nil, 0, false
	}
	done := make(chan struct{})
	overflowVersion := s.sub.overflow.Load()
	queued := s.sub.enqueueFence(done)
	s.bus.mu.RUnlock()
	if !queued {
		return nil, 0, false
	}
	return done, overflowVersion, true
}

func (s *seqFenceSubscription) Done() <-chan struct{} { return s.sub.done }
func (s *seqFenceSubscription) Overflowed() bool {
	return s.sub.stopped.Load() || overflowPending(s.sub.overflow.Load(), s.sub.clearedOverflow.Load())
}
func (s *seqFenceSubscription) OverflowVersion() uint64 { return s.sub.overflow.Load() }
func (s *seqFenceSubscription) ClearOverflowThrough(version uint64) {
	s.ClearOverflowThroughContext(context.Background(), version)
}

// ClearOverflowThroughContext is the cancellable form used by serve's
// deferred overflow-clear worker. It never waits indefinitely for publishMu
// after its session or subscription has ended.
func (s *seqFenceSubscription) ClearOverflowThroughContext(ctx context.Context, version uint64) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if s.bus.publishMu.TryLock() {
			if ctx.Err() != nil || s.bus.closed.Load() || s.sub.stopped.Load() {
				s.bus.publishMu.Unlock()
				return false
			}
			for {
				cleared := s.sub.clearedOverflow.Load()
				if overflowVersionAtOrAfter(cleared, version) || s.sub.clearedOverflow.CompareAndSwap(cleared, version) {
					s.bus.publishMu.Unlock()
					return true
				}
			}
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return false
		case <-s.sub.done:
			if !timer.Stop() {
				<-timer.C
			}
			return false
		case <-timer.C:
		}
	}
}

func (s *seqFenceSubscription) ClearOverflowThroughBefore(version uint64, deadline time.Time) bool {
	if !tryLockBefore(&s.bus.publishMu, deadline) {
		return false
	}
	defer s.bus.publishMu.Unlock()
	if !time.Now().Before(deadline) {
		return false
	}
	for {
		cleared := s.sub.clearedOverflow.Load()
		if overflowVersionAtOrAfter(cleared, version) || s.sub.clearedOverflow.CompareAndSwap(cleared, version) {
			return true
		}
	}
}

// tryLockBefore avoids letting a mutex wait extend a caller's latency budget.
// Mutex acquisition itself is not cancellable, so poll in short intervals.
func tryLockBefore(mu *sync.Mutex, deadline time.Time) bool {
	for {
		if mu.TryLock() {
			if time.Now().Before(deadline) {
				return true
			}
			mu.Unlock()
			return false
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		if remaining > time.Millisecond {
			remaining = time.Millisecond
		}
		timer := time.NewTimer(remaining)
		<-timer.C
	}
}

// overflowVersionAtOrAfter compares serial numbers rather than ordinary
// unsigned integers. Overflow versions advance one at a time, so the usual
// half-range serial-number ordering preserves their order across uint64 wrap.
func overflowVersionAtOrAfter(a, b uint64) bool {
	return int64(a-b) >= 0
}

func overflowPending(overflow, cleared uint64) bool {
	return overflow != cleared && overflowVersionAtOrAfter(overflow, cleared)
}

type stoppedSeqFenceSubscription struct{}

func (stoppedSeqFenceSubscription) Unsubscribe() {}
func (stoppedSeqFenceSubscription) CaptureCut() (uint64, uint64, uint64, bool) {
	return 0, 0, 0, false
}
func (stoppedSeqFenceSubscription) CaptureCutBefore(time.Time) (uint64, uint64, uint64, bool) {
	return 0, 0, 0, false
}
func (stoppedSeqFenceSubscription) Fence() (<-chan struct{}, uint64, bool) {
	return nil, 0, false
}
func (stoppedSeqFenceSubscription) FenceBefore(time.Time) (<-chan struct{}, uint64, bool) {
	return nil, 0, false
}
func (stoppedSeqFenceSubscription) Done() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
func (stoppedSeqFenceSubscription) Overflowed() bool            { return true }
func (stoppedSeqFenceSubscription) OverflowVersion() uint64     { return 1 }
func (stoppedSeqFenceSubscription) ClearOverflowThrough(uint64) {}
func (stoppedSeqFenceSubscription) ClearOverflowThroughContext(context.Context, uint64) bool {
	return false
}
func (stoppedSeqFenceSubscription) ClearOverflowThroughBefore(uint64, time.Time) bool {
	return false
}

// LastSeq implements EventBus.LastSeq.
func (b *LocalBus) LastSeq() uint64 {
	b.publishMu.Lock()
	defer b.publishMu.Unlock()
	return b.seq.Load()
}

// CaptureSeq implements EventBus.CaptureSeq.
func (b *LocalBus) CaptureSeq() uint64 {
	b.publishMu.Lock()
	defer b.publishMu.Unlock()
	return b.seq.Load()
}

// Publish implements EventBus.
func (b *LocalBus) Publish(event any) {
	if event == nil {
		panic("bus: nil event")
	}
	b.publishMu.Lock()
	defer b.publishMu.Unlock()
	if b.closed.Load() {
		return
	}
	b.published.Store(true)
	seq := b.seq.Add(1)
	et := reflect.TypeOf(event)

	lossy := isLossyEvent(event)

	// Hold read lock during the entire enqueue loop. enqueue never blocks (it
	// takes only the subscriber's local mutex briefly), so this is cheap and
	// cannot deadlock. The stopped check ensures we skip subscribers whose
	// goroutine is shutting down (unsubscribe/close take the write lock first).
	b.mu.RLock()

	// SubscribeAll handlers first — guarantees they see events before typed subs.
	for _, sub := range b.allSubs {
		if sub.stopped.Load() {
			continue
		}
		b.inflight.Add(1)
		if !sub.enqueue(seq, event, lossy) {
			b.decrementInflight() // lossy event dropped under backpressure
		}
	}
	for _, sub := range b.allSeqSubs {
		if sub.stopped.Load() {
			continue
		}
		queued := event
		if sub.isAttentionSeq {
			var accepted bool
			queued, accepted = projectAttentionSequenceEvent(event)
			if !accepted {
				continue
			}
		}
		b.inflight.Add(1)
		if !sub.enqueue(seq, queued, lossy) {
			b.decrementInflight()
		}
	}

	// Typed subscribers.
	for _, sub := range b.eventSubs[et] {
		if sub.stopped.Load() {
			continue // skip subscribers in the process of shutting down
		}
		b.inflight.Add(1)
		if !sub.enqueue(seq, event, lossy) {
			b.decrementInflight() // lossy event dropped under backpressure
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
	allSubs := make([]*subscriber, 0, len(b.allSubs)+len(b.allSeqSubs))
	allSubs = append(allSubs, b.allSubs...)
	allSubs = append(allSubs, b.allSeqSubs...)
	for _, subs := range b.eventSubs {
		allSubs = append(allSubs, subs...)
	}
	b.allSubs = nil
	b.allSeqSubs = nil
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

// enqueue appends an event to the subscriber's queue and wakes its goroutine.
// A correctness subscription may impose its own bounded queue; its owner sees
// an overflow and must resynchronize rather than accepting a missing event.
// Ordinary lossless subscriptions retain their established delivery semantics.
func (s *subscriber) enqueue(seq uint64, event any, lossy bool) bool {
	s.mu.Lock()
	if s.queueCap > 0 && len(s.queue) >= s.queueCap {
		s.overflow.Add(1)
		s.mu.Unlock()
		return false
	}
	if lossy && len(s.queue) >= subscriberBuffer {
		s.mu.Unlock()
		return false
	}
	s.queue = append(s.queue, queuedEvent{seq: seq, event: event})
	s.mu.Unlock()

	// Wake the goroutine; a single pending signal is enough.
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return true
}

// enqueueFence appends a completion marker without affecting the bus inflight
// count. Callers hold the bus publication lock, so matching events published
// before this marker are already ahead of it in this FIFO.
func (s *subscriber) enqueueFence(done chan struct{}) bool {
	s.mu.Lock()
	if s.queueCap > 0 && len(s.queue) >= s.queueCap {
		s.overflow.Add(1)
		s.mu.Unlock()
		return false
	}
	s.queue = append(s.queue, queuedEvent{fence: done})
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return true
}

func (s *subscriber) loop() {
	defer close(s.exited)
	for {
		select {
		case <-s.notify:
			s.drain()
		case <-s.done:
			if !s.discardOnStop {
				s.drain() // process everything queued before exiting
			}
			return
		}
	}
}

// drain processes all currently-queued events in FIFO order. Events enqueued
// while draining are picked up on the next loop iteration.
func (s *subscriber) drain() {
	for {
		s.mu.Lock()
		if len(s.queue) == 0 {
			s.mu.Unlock()
			return
		}
		batch := s.queue
		s.queue = nil
		s.mu.Unlock()
		for _, event := range batch {
			if event.fence != nil {
				close(event.fence)
				continue
			}
			s.process(event)
		}
	}
}

func (s *subscriber) process(queued queuedEvent) {
	defer s.bus.decrementInflight()
	defer func() { _ = recover() }() // swallow handler panics
	if s.isAll {
		// SubscribeAll handler: fn is func(any), call directly for efficiency.
		s.fn.Interface().(func(any))(queued.event)
	} else if s.isSeqAll {
		s.fn.Interface().(func(uint64, any))(queued.seq, queued.event)
	} else if s.isAttentionSeq {
		s.fn.Interface().(func(uint64, AttentionSequenceEvent))(queued.seq, queued.event.(AttentionSequenceEvent))
	} else {
		s.fn.Call([]reflect.Value{reflect.ValueOf(queued.event)})
	}
}

func projectAttentionSequenceEvent(event any) (AttentionSequenceEvent, bool) {
	switch event := event.(type) {
	case RunEnded:
		if event.Cancelled && event.Err == nil {
			return AttentionSequenceEvent{}, false
		}
		return AttentionSequenceEvent{
			Kind: AttentionRunEnded, Cancelled: event.Cancelled, Errored: event.Err != nil,
		}, true
	case PermissionRequested:
		return AttentionSequenceEvent{Kind: AttentionPermissionRequested}, true
	case AskUserRequested:
		return AttentionSequenceEvent{Kind: AttentionAskUserRequested}, true
	case StateChanged:
		if event.State == string(StateError) {
			return AttentionSequenceEvent{Kind: AttentionStateError}, true
		}
	}
	return AttentionSequenceEvent{}, false
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
