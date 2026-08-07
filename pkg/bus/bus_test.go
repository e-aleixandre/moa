package bus

import (
	"errors"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProjectedFilteredSubscriptionBoundsOverflowAndDiscardsOnTeardown(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	subscription := b.SubscribeAttentionSeq(
		func(_ uint64, event AttentionSequenceEvent) {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
		},
	)
	sub := subscription.(*seqFenceSubscription).sub

	b.Publish(PermissionRequested{ID: "first"})
	<-entered
	// Each publication has a large backing string, but the queued projection is
	// only AttentionSequenceEvent. A stuck handler cannot retain final output.
	for i := 0; i < correctnessSubscriberBuffer+20; i++ {
		b.Publish(RunEnded{FinalText: string(make([]byte, 1<<20))})
	}
	if !subscription.Overflowed() {
		t.Fatal("bounded correctness queue did not report overflow")
	}
	sub.mu.Lock()
	queued := append([]queuedEvent(nil), sub.queue...)
	sub.mu.Unlock()
	if len(queued) > correctnessSubscriberBuffer {
		t.Fatalf("queued events = %d, want at most %d", len(queued), correctnessSubscriberBuffer)
	}
	for _, event := range queued {
		if _, ok := event.event.(AttentionSequenceEvent); !ok {
			t.Fatalf("retained projected event has type %T, want AttentionSequenceEvent", event.event)
		}
	}

	subscription.Unsubscribe()
	sub.mu.Lock()
	remaining := len(sub.queue)
	sub.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("teardown retained %d queued events", remaining)
	}
	close(release)
	b.Drain(time.Second)
	runtime.GC()
}

func TestAttentionSubscriptionHandlerCanUseBusOperations(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	done := make(chan struct{})
	var subscription SeqFenceSubscription
	subscription = b.SubscribeAttentionSeq(func(_ uint64, _ AttentionSequenceEvent) {
		// The fixed projection is delivered asynchronously, after Publish has
		// released its locks, so these operations cannot recurse into a
		// publisher-held callback.
		b.Publish(testEvent{Value: "from attention handler"})
		if _, _, ok := subscription.Fence(); !ok {
			t.Error("handler could not place a fence")
		}
		unsubscribe := b.Subscribe(func(testEvent) {})
		unsubscribe()
		subscription.Unsubscribe()
		close(done)
	})
	b.Publish(PermissionRequested{ID: "p1"})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("attention handler deadlocked using bus operations")
	}
}

func TestAttentionCutAndOverflowClearRespectDeadline(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	subscription := b.SubscribeAttentionSeq(func(uint64, AttentionSequenceEvent) {})

	b.publishMu.Lock()
	cutDone := make(chan bool, 1)
	go func() {
		_, _, _, ok := subscription.CaptureCutBefore(time.Now().Add(25 * time.Millisecond))
		cutDone <- ok
	}()
	select {
	case ok := <-cutDone:
		if ok {
			t.Fatal("CaptureCutBefore succeeded while publication lock was held")
		}
	case <-time.After(time.Second):
		b.publishMu.Unlock()
		t.Fatal("CaptureCutBefore waited for publication lock past its deadline")
	}

	clearDone := make(chan bool, 1)
	go func() {
		clearDone <- subscription.ClearOverflowThroughBefore(1, time.Now().Add(25*time.Millisecond))
	}()
	select {
	case ok := <-clearDone:
		if ok {
			t.Fatal("ClearOverflowThroughBefore succeeded while publication lock was held")
		}
	case <-time.After(time.Second):
		b.publishMu.Unlock()
		t.Fatal("ClearOverflowThroughBefore waited for publication lock past its deadline")
	}
	b.publishMu.Unlock()
}

func TestAttentionCutInitialZeroIsNotWrappedZero(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	subscription := b.SubscribeAttentionSeq(func(uint64, AttentionSequenceEvent) {})
	capturer, ok := subscription.(initialCutCapturer)
	if !ok {
		t.Fatal("LocalBus attention subscription cannot report initial cuts")
	}
	cut, _, _, initial, captured := capturer.CaptureCutWithInitial()
	if !captured || cut != 0 || !initial {
		t.Fatalf("fresh capture = (cut:%d initial:%v captured:%v), want (0, true, true)", cut, initial, captured)
	}

	// Model the state immediately after uint64 sequence wrap. A zero sequence
	// alone must never be interpreted as the pristine no-event boundary.
	b.publishMu.Lock()
	b.seq.Store(0)
	b.published.Store(true)
	b.publishMu.Unlock()
	cut, _, _, initial, captured = capturer.CaptureCutWithInitial()
	if !captured || cut != 0 || initial {
		t.Fatalf("wrapped capture = (cut:%d initial:%v captured:%v), want (0, false, true)", cut, initial, captured)
	}
}

func TestStoppedAttentionSubscriptionFailsClosed(t *testing.T) {
	b := NewLocalBus()
	subscription := b.SubscribeAttentionSeq(func(uint64, AttentionSequenceEvent) {})
	subscription.Unsubscribe()
	if !subscription.Overflowed() {
		t.Fatal("unsubscribed correctness subscription did not fail closed")
	}

	b = NewLocalBus()
	subscription = b.SubscribeAttentionSeq(func(uint64, AttentionSequenceEvent) {})
	b.Close()
	if !subscription.Overflowed() {
		t.Fatal("closed correctness subscription did not fail closed")
	}
}

func TestAttentionOverflowRemainsPendingAcrossUint64Wrap(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	subscription := b.SubscribeAttentionSeq(func(uint64, AttentionSequenceEvent) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
	})
	sub := subscription.(*seqFenceSubscription).sub
	// Start one increment before the boundary with no pending overflow. The
	// next two dropped occurrences produce MaxUint64 then zero.
	sub.overflow.Store(math.MaxUint64 - 1)
	sub.clearedOverflow.Store(math.MaxUint64 - 1)
	b.Publish(PermissionRequested{ID: "block"})
	<-entered
	for i := 0; i < correctnessSubscriberBuffer+1; i++ {
		b.Publish(PermissionRequested{ID: "max"})
	}
	if !subscription.Overflowed() {
		t.Fatal("overflow at MaxUint64 was not pending")
	}
	subscription.ClearOverflowThrough(math.MaxUint64)
	if subscription.Overflowed() {
		t.Fatal("clearing MaxUint64 did not settle its overflow")
	}
	b.Publish(PermissionRequested{ID: "wrapped"})
	if sub.overflow.Load() != 0 {
		t.Fatalf("overflow version = %d, want wrapped zero", sub.overflow.Load())
	}
	if !subscription.Overflowed() {
		t.Fatal("overflow after uint64 wrap was treated as cleared")
	}
	close(release)
}

// ---------------------------------------------------------------------------
// Test helpers — tiny event/command/query types local to tests
// ---------------------------------------------------------------------------

type testEvent struct{ Value string }
type testEvent2 struct{ N int }

type testCmd struct{ X int }
type testQuery struct{ Key string }

// ---------------------------------------------------------------------------
// Subscribe + Publish
// ---------------------------------------------------------------------------

func TestPublish_CorrectType(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	got := make(chan testEvent, 1)
	b.Subscribe(func(e testEvent) { got <- e })

	b.Publish(testEvent{Value: "hello"})
	b.Drain(time.Second)

	select {
	case e := <-got:
		if e.Value != "hello" {
			t.Fatalf("got %q, want %q", e.Value, "hello")
		}
	default:
		t.Fatal("handler not called")
	}
}

func TestSubscribeAllSeq_OrdersPublicationsAndReportsBoundary(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	type received struct {
		seq   uint64
		value string
	}
	got := make(chan received, 2)
	b.SubscribeAllSeq(func(seq uint64, event any) {
		got <- received{seq: seq, value: event.(testEvent).Value}
	})
	b.Publish(testEvent{Value: "first"})
	if cut := b.LastSeq(); cut != 1 {
		t.Fatalf("LastSeq after first publish = %d, want 1", cut)
	}
	b.Publish(testEvent{Value: "second"})
	b.Drain(time.Second)
	for _, want := range []received{{seq: 1, value: "first"}, {seq: 2, value: "second"}} {
		e := <-got
		if e != want {
			t.Fatalf("event = %#v, want %#v", e, want)
		}
	}
}

func TestSubscribeAllSeqHandlerCanPublishAndCaptureSequence(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	var captured atomic.Uint64
	done := make(chan struct{})
	b.SubscribeAllSeq(func(_ uint64, event any) {
		if event.(testEvent).Value != "first" {
			return
		}
		captured.Store(b.CaptureSeq())
		b.Publish(testEvent{Value: "second"})
		close(done)
	})
	b.Publish(testEvent{Value: "first"})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sequence subscriber deadlocked while publishing")
	}
	b.Drain(time.Second)
	if got := captured.Load(); got != 1 {
		t.Fatalf("captured sequence = %d, want 1", got)
	}
	if got := b.LastSeq(); got != 2 {
		t.Fatalf("last sequence = %d, want 2", got)
	}
}

func TestAttentionGenerationCaptureDrainsTheSampleCutInterleaving(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	var attentionGen atomic.Uint64
	var pending atomic.Bool
	var eventSeq atomic.Uint64
	var seqMu sync.Mutex
	seqGens := make(map[uint64]uint64)
	b.SubscribeAllSeq(func(seq uint64, event any) {
		switch event {
		case testEvent{Value: "attention-before"}:
			attentionGen.Store(1)
			eventSeq.Store(seq)
			seqMu.Lock()
			seqGens[seq] = 1
			seqMu.Unlock()
		case testEvent{Value: "attention-after"}:
			attentionGen.Store(2)
			seqMu.Lock()
			seqGens[seq] = 2
			seqMu.Unlock()
		}
	})

	// Reproduce the old window: the pending request is installed and its event
	// is accepted after a stale generation sample but before the old LastSeq
	// cut. Holding publishMu lets the test place those two operations exactly.
	b.publishMu.Lock()
	staleGen := attentionGen.Load()
	pending.Store(true)
	published := make(chan struct{})
	go func() {
		b.Publish(testEvent{Value: "attention-before"})
		close(published)
	}()
	b.publishMu.Unlock()
	<-published

	var snapshotShowsPending bool
	var initGen, cut uint64
	cut = b.LastSeq()
	// A newer occurrence after the cut must not leak into init's bound.
	b.Publish(testEvent{Value: "attention-after"})
	b.Drain(time.Second)
	snapshotShowsPending = pending.Load()
	seqMu.Lock()
	for seq, gen := range seqGens {
		if seq <= cut && seq >= eventSeq.Load() {
			initGen = gen
		}
	}
	seqMu.Unlock()

	if staleGen != 0 {
		t.Fatalf("stale generation = %d, want 0", staleGen)
	}
	if !snapshotShowsPending || eventSeq.Load() > cut {
		t.Fatalf("test did not place the attention event in the snapshot boundary: pending=%v seq=%d cut=%d", snapshotShowsPending, eventSeq.Load(), cut)
	}
	if initGen != 1 {
		t.Fatalf("init unseen generation = %d, want generation 1 for pending attention at seq %d", initGen, eventSeq.Load())
	}
	if attentionGen.Load() != 2 {
		t.Fatalf("latest generation = %d, want post-cut generation 2", attentionGen.Load())
	}
}

func TestPublish_WrongType(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	called := make(chan struct{}, 1)
	b.Subscribe(func(e testEvent) { called <- struct{}{} })

	b.Publish(testEvent2{N: 42})
	b.Drain(time.Second)

	select {
	case <-called:
		t.Fatal("handler should not be called for different event type")
	default:
		// good
	}
}

func TestPublish_MultipleSubscribers(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	var count atomic.Int32
	b.Subscribe(func(e testEvent) { count.Add(1) })
	b.Subscribe(func(e testEvent) { count.Add(1) })

	b.Publish(testEvent{Value: "x"})
	b.Drain(time.Second)

	if c := count.Load(); c != 2 {
		t.Fatalf("got %d calls, want 2", c)
	}
}

func TestPublish_Unsubscribe(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	var count atomic.Int32
	unsub := b.Subscribe(func(e testEvent) { count.Add(1) })

	b.Publish(testEvent{Value: "a"})
	b.Drain(time.Second)
	if c := count.Load(); c != 1 {
		t.Fatalf("got %d, want 1 before unsubscribe", c)
	}

	unsub()
	unsub() // idempotent — no panic

	b.Publish(testEvent{Value: "b"})
	b.Drain(time.Second)
	if c := count.Load(); c != 1 {
		t.Fatalf("got %d, want 1 after unsubscribe", c)
	}
}

func TestPublish_NoSubscribers(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()
	// Should not panic.
	b.Publish(testEvent{Value: "nobody listening"})
	b.Drain(time.Second)
}

func TestPublish_HandlerPanic(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	var count atomic.Int32
	// First subscriber panics.
	b.Subscribe(func(e testEvent) { panic("boom") })
	// Second subscriber should still be called (different goroutine).
	b.Subscribe(func(e testEvent) { count.Add(1) })

	b.Publish(testEvent{Value: "x"})
	b.Drain(time.Second)

	if c := count.Load(); c != 1 {
		t.Fatalf("second subscriber should still run, got count=%d", c)
	}
}

func TestPublish_BufferFull(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	// Block the subscriber goroutine.
	block := make(chan struct{})
	b.Subscribe(func(e testEvent) { <-block })

	// Publish one to occupy the goroutine, then fill the buffer.
	b.Publish(testEvent{Value: "occupier"})
	time.Sleep(10 * time.Millisecond) // let goroutine pick it up

	// Fill the buffer (subscriberBuffer = 256).
	for i := 0; i < subscriberBuffer+10; i++ {
		b.Publish(testEvent{Value: "overflow"})
	}

	// Should not deadlock — test will timeout if it does.
	close(block)
	b.Drain(time.Second)
}

// TestPublish_LosslessSurvivesBackpressure verifies the lost-structural-event
// fix: when a slow subscriber falls behind under a flood of lossy deltas, the
// deltas are capped/dropped but a lossless structural event (StateChanged)
// published afterwards is still delivered. Before the fix, a full buffer
// dropped everything indiscriminately, wedging the UI in "running".
func TestPublish_LosslessSurvivesBackpressure(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var mu sync.Mutex
	var lossyProcessed int
	var sawState bool

	b.SubscribeAll(func(ev any) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release // block so the queue fills while we publish
		mu.Lock()
		switch ev.(type) {
		case TextDelta:
			lossyProcessed++
		case StateChanged:
			sawState = true
		}
		mu.Unlock()
	})

	// Occupy the goroutine with one lossy event so it blocks on <-release.
	b.Publish(TextDelta{Delta: "occupier"})
	<-started

	// Flood with lossy deltas well past the cap — most must be dropped.
	for i := 0; i < subscriberBuffer+500; i++ {
		b.Publish(TextDelta{Delta: "x"})
	}
	// Publish a lossless structural event AFTER the flood.
	b.Publish(StateChanged{SessionID: "s", State: "idle"})

	close(release)
	b.Drain(2 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	if !sawState {
		t.Fatal("StateChanged (lossless) was dropped under backpressure — must never happen")
	}
	// occupier + at most subscriberBuffer queued lossy events survive.
	if lossyProcessed < 1 {
		t.Fatal("expected at least the occupier lossy event")
	}
	if lossyProcessed > subscriberBuffer+1 {
		t.Fatalf("processed %d lossy events, want <= %d (cap not enforced)", lossyProcessed, subscriberBuffer+1)
	}
}

func TestPublish_AfterClose(t *testing.T) {
	b := NewLocalBus()
	called := make(chan struct{}, 1)
	b.Subscribe(func(e testEvent) { called <- struct{}{} })
	b.Close()

	// Should not panic.
	b.Publish(testEvent{Value: "after close"})
	time.Sleep(50 * time.Millisecond)

	select {
	case <-called:
		t.Fatal("should not deliver after close")
	default:
	}
}

// ---------------------------------------------------------------------------
// OnCommand + Execute
// ---------------------------------------------------------------------------

func TestExecute_Success(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	var got int
	b.OnCommand(func(c testCmd) error {
		got = c.X
		return nil
	})

	if err := b.Execute(testCmd{X: 42}); err != nil {
		t.Fatal(err)
	}
	if got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
}

func TestExecute_Error(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	b.OnCommand(func(c testCmd) error {
		return errors.New("fail")
	})

	err := b.Execute(testCmd{X: 1})
	if err == nil || err.Error() != "fail" {
		t.Fatalf("got %v, want 'fail'", err)
	}
}

func TestExecute_NoHandler(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	err := b.Execute(testCmd{X: 1})
	if !errors.Is(err, ErrNoHandler) {
		t.Fatalf("got %v, want ErrNoHandler", err)
	}
}

func TestExecute_DuplicateHandler(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	b.OnCommand(func(c testCmd) error { return nil })
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on duplicate command handler")
		}
	}()
	b.OnCommand(func(c testCmd) error { return nil })
}

func TestExecute_HandlerPanic(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	b.OnCommand(func(c testCmd) error {
		panic("command boom")
	})

	err := b.Execute(testCmd{X: 1})
	if err == nil {
		t.Fatal("expected error from panicking handler")
	}
	if got := err.Error(); got != "bus: command handler panic: command boom" {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestExecute_AfterClose(t *testing.T) {
	b := NewLocalBus()
	b.OnCommand(func(c testCmd) error { return nil })
	b.Close()

	err := b.Execute(testCmd{X: 1})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v, want ErrClosed", err)
	}
}

// ---------------------------------------------------------------------------
// OnQuery + Query
// ---------------------------------------------------------------------------

func TestQuery_Success(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	b.OnQuery(func(q testQuery) (string, error) {
		return "val:" + q.Key, nil
	})

	result, err := b.Query(testQuery{Key: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	s, ok := result.(string)
	if !ok || s != "val:abc" {
		t.Fatalf("got %v, want 'val:abc'", result)
	}
}

func TestQuery_NoHandler(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	_, err := b.Query(testQuery{Key: "x"})
	if !errors.Is(err, ErrNoHandler) {
		t.Fatalf("got %v, want ErrNoHandler", err)
	}
}

func TestQuery_DuplicateHandler(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	b.OnQuery(func(q testQuery) (string, error) { return "", nil })
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on duplicate query handler")
		}
	}()
	b.OnQuery(func(q testQuery) (string, error) { return "", nil })
}

func TestQuery_HandlerPanic(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	b.OnQuery(func(q testQuery) (string, error) {
		panic("query boom")
	})

	result, err := b.Query(testQuery{Key: "x"})
	if err == nil {
		t.Fatal("expected error from panicking handler")
	}
	if result != nil {
		t.Fatalf("expected nil result, got %v", result)
	}
	if got := err.Error(); got != "bus: query handler panic: query boom" {
		t.Fatalf("unexpected error: %s", got)
	}
}

func TestQuery_AfterClose(t *testing.T) {
	b := NewLocalBus()
	b.OnQuery(func(q testQuery) (string, error) { return "x", nil })
	b.Close()

	_, err := b.Query(testQuery{Key: "x"})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v, want ErrClosed", err)
	}
}

// ---------------------------------------------------------------------------
// QueryTyped helper
// ---------------------------------------------------------------------------

func TestQueryTyped_Success(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	b.OnQuery(func(q testQuery) (string, error) {
		return "typed:" + q.Key, nil
	})

	got, err := QueryTyped[testQuery, string](b, testQuery{Key: "z"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "typed:z" {
		t.Fatalf("got %q, want %q", got, "typed:z")
	}
}

func TestQueryTyped_Error(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	_, err := QueryTyped[testQuery, string](b, testQuery{Key: "x"})
	if !errors.Is(err, ErrNoHandler) {
		t.Fatalf("got %v, want ErrNoHandler", err)
	}
}

// ---------------------------------------------------------------------------
// Registration validation
// ---------------------------------------------------------------------------

func TestSubscribe_InvalidSignature_NoParams(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	b.Subscribe(func() {})
}

func TestSubscribe_InvalidSignature_TooManyParams(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	b.Subscribe(func(a, b int) {})
}

func TestSubscribe_PointerParam(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		s, ok := r.(string)
		if !ok || s != "bus: handler parameter must be a struct, not a pointer" {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	b.Subscribe(func(e *testEvent) {})
}

func TestOnCommand_InvalidSignature(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	// Returns string instead of error.
	b.OnCommand(func(x int) string { return "" })
}

func TestOnQuery_InvalidSignature(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	// No return values.
	b.OnQuery(func(x int) {})
}

// ---------------------------------------------------------------------------
// Nil inputs
// ---------------------------------------------------------------------------

func TestPublish_Nil(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		if s, ok := r.(string); !ok || s != "bus: nil event" {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	b.Publish(nil)
}

func TestExecute_Nil(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		if s, ok := r.(string); !ok || s != "bus: nil command" {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	_ = b.Execute(nil)
}

func TestQuery_Nil(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		if s, ok := r.(string); !ok || s != "bus: nil query" {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	_, _ = b.Query(nil)
}

// ---------------------------------------------------------------------------
// Close lifecycle
// ---------------------------------------------------------------------------

func TestClose_Idempotent(t *testing.T) {
	b := NewLocalBus()
	b.Close()
	b.Close() // should not panic
}

func TestClose_DrainsPending(t *testing.T) {
	b := NewLocalBus()

	var count atomic.Int32
	b.Subscribe(func(e testEvent) {
		time.Sleep(5 * time.Millisecond)
		count.Add(1)
	})

	for i := 0; i < 10; i++ {
		b.Publish(testEvent{Value: "x"})
	}

	b.Close() // should wait for goroutines to drain

	// All 10 should have been processed (Close drains remaining).
	if c := count.Load(); c != 10 {
		t.Fatalf("got %d processed, want 10", c)
	}
}

func TestClose_ConcurrentPublish(t *testing.T) {
	b := NewLocalBus()
	b.Subscribe(func(e testEvent) {})

	var wg sync.WaitGroup
	// Goroutine publishing in a loop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			b.Publish(testEvent{Value: "x"})
		}
	}()

	// Close from another goroutine.
	time.Sleep(time.Millisecond)
	b.Close()
	wg.Wait()
	// No race, no panic.
}

// ---------------------------------------------------------------------------
// Drain
// ---------------------------------------------------------------------------

func TestDrain_WaitsForCompletion(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	var count atomic.Int32
	b.Subscribe(func(e testEvent) {
		time.Sleep(10 * time.Millisecond)
		count.Add(1)
	})

	for i := 0; i < 5; i++ {
		b.Publish(testEvent{Value: "x"})
	}

	b.Drain(5 * time.Second)
	if c := count.Load(); c != 5 {
		t.Fatalf("got %d, want 5 after Drain", c)
	}
}

func TestDrain_Timeout(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	block := make(chan struct{})
	b.Subscribe(func(e testEvent) { <-block })

	b.Publish(testEvent{Value: "x"})
	time.Sleep(10 * time.Millisecond) // let goroutine pick it up

	start := time.Now()
	b.Drain(50 * time.Millisecond)
	elapsed := time.Since(start)

	if elapsed < 40*time.Millisecond {
		t.Fatalf("Drain returned too fast: %v", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Drain took too long: %v", elapsed)
	}

	close(block)
}

// ---------------------------------------------------------------------------
// Concurrency (validated via -race)
// ---------------------------------------------------------------------------

func TestConcurrent_PublishFromMultipleGoroutines(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	var count atomic.Int32
	b.Subscribe(func(e testEvent) { count.Add(1) })

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Publish(testEvent{Value: "x"})
		}()
	}
	wg.Wait()
	b.Drain(5 * time.Second)

	if c := count.Load(); c != 100 {
		t.Fatalf("got %d, want 100", c)
	}
}

func TestConcurrent_SubscribeUnsubscribeDuringPublish(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	var wg sync.WaitGroup

	// Goroutine that publishes continuously.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			b.Publish(testEvent{Value: "x"})
			time.Sleep(100 * time.Microsecond)
		}
	}()

	// Goroutine that subscribes and unsubscribes.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			unsub := b.Subscribe(func(e testEvent) {})
			time.Sleep(200 * time.Microsecond)
			unsub()
		}
	}()

	wg.Wait()
	b.Drain(time.Second)
}

func TestConcurrent_ExecuteFromMultipleGoroutines(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	var count atomic.Int32
	b.OnCommand(func(c testCmd) error {
		count.Add(1)
		return nil
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = b.Execute(testCmd{X: n})
		}(i)
	}
	wg.Wait()

	if c := count.Load(); c != 100 {
		t.Fatalf("got %d, want 100", c)
	}
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

func TestNewLocalBus(t *testing.T) {
	b := NewLocalBus()
	if b == nil {
		t.Fatal("NewLocalBus returned nil")
	}
	if b.eventSubs == nil || b.cmdH == nil || b.queryH == nil {
		t.Fatal("internal maps not initialized")
	}
	b.Close()
}

func TestZeroValue_Panics(t *testing.T) {
	b := &LocalBus{} // zero value, no constructor
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on zero-value bus")
		}
	}()
	b.Subscribe(func(e testEvent) {})
}

// ---------------------------------------------------------------------------
// Close + Unsubscribe race
// ---------------------------------------------------------------------------

func TestClose_ConcurrentUnsubscribe(t *testing.T) {
	// Close and unsubscribe racing on the same subscriber must not panic.
	for i := 0; i < 100; i++ {
		b := NewLocalBus()
		unsub := b.Subscribe(func(e testEvent) {})

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			b.Close()
		}()
		go func() {
			defer wg.Done()
			unsub()
		}()
		wg.Wait()
	}
}

// ---------------------------------------------------------------------------
// Publish + Unsubscribe inflight leak
// ---------------------------------------------------------------------------

func TestPublish_UnsubscribeDuringPublish_DrainReachesZero(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	var count atomic.Int32
	unsub := b.Subscribe(func(e testEvent) {
		time.Sleep(time.Millisecond)
		count.Add(1)
	})

	// Publish several events.
	for i := 0; i < 20; i++ {
		b.Publish(testEvent{Value: "x"})
	}

	// Unsubscribe while events may still be in-flight.
	unsub()

	// Drain must return promptly (not timeout) — no inflight leaks.
	start := time.Now()
	b.Drain(2 * time.Second)
	if time.Since(start) > time.Second {
		t.Fatal("Drain took too long — possible inflight leak")
	}
}

// ---------------------------------------------------------------------------
// Subscribe after Close
// ---------------------------------------------------------------------------

func TestSubscribe_AfterClose(t *testing.T) {
	b := NewLocalBus()
	b.Close()

	called := make(chan struct{}, 1)
	unsub := b.Subscribe(func(e testEvent) { called <- struct{}{} })

	// unsub should be a no-op, not nil.
	unsub()
	unsub() // idempotent

	// Publish should also be no-op.
	b.Publish(testEvent{Value: "x"})
	time.Sleep(50 * time.Millisecond)

	select {
	case <-called:
		t.Fatal("handler should not be called after close")
	default:
	}
}

// ---------------------------------------------------------------------------
// Unsubscribe from within handler
// ---------------------------------------------------------------------------

func TestUnsubscribe_FromHandler(t *testing.T) {
	b := NewLocalBus()
	defer b.Close()

	done := make(chan struct{})
	var unsub func()
	unsub = b.Subscribe(func(e testEvent) {
		unsub() // must not deadlock
		close(done)
	})

	b.Publish(testEvent{Value: "self-unsub"})

	select {
	case <-done:
		// good — no deadlock
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: unsubscribe from handler blocked")
	}
}
