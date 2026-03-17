package bus

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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
// Close + Unsubscribe race (review issue #1)
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
// Publish + Unsubscribe inflight leak (review issue #2)
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
// Subscribe after Close (review issue #3)
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
// Unsubscribe from within handler (review issue #4)
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
