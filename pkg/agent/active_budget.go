package agent

import (
	"context"
	"sync"
	"time"
)

// A cancelable root replaces the child's non-extendable WithTimeout. The
// watchdog accounts monotonic active time and cannot revive an expired root.
type activeBudget struct {
	mu                      sync.Mutex
	ctx                     context.Context
	cancel                  context.CancelFunc
	timer                   *time.Timer
	remaining               time.Duration
	started                 time.Time
	paused, expired, closed bool
}

type budgetContext struct {
	context.Context
	budget *activeBudget
}

func (c budgetContext) Err() error {
	err := c.Context.Err()
	if err == nil {
		return nil
	}
	c.budget.mu.Lock()
	expired := c.budget.expired
	c.budget.mu.Unlock()
	if expired {
		return context.DeadlineExceeded
	}
	return err
}

func newActiveBudget(parent context.Context, duration time.Duration) (context.Context, *activeBudget) {
	ctx, cancel := context.WithCancel(parent)
	b := &activeBudget{ctx: ctx, cancel: cancel, remaining: duration, started: time.Now()}
	b.timer = time.AfterFunc(duration, b.expire)
	return budgetContext{Context: ctx, budget: b}, b
}

func (b *activeBudget) expire() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.paused || b.ctx.Err() != nil {
		return
	}
	if left := b.remaining - time.Since(b.started); left > 0 {
		b.timer.Reset(left)
		return
	}
	b.expired = true
	b.cancel()
}

func (b *activeBudget) pause() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if b.expired || b.closed {
		return context.DeadlineExceeded
	}
	if b.paused {
		return nil
	}
	b.remaining -= time.Since(b.started)
	if b.remaining <= 0 {
		b.expired = true
		b.cancel()
		return context.DeadlineExceeded
	}
	b.paused = true
	b.timer.Stop()
	return nil
}

func (b *activeBudget) resume() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.expired || b.ctx.Err() != nil || !b.paused {
		return
	}
	b.paused = false
	b.started = time.Now()
	b.timer.Reset(b.remaining)
}

func (b *activeBudget) close() {
	b.mu.Lock()
	b.closed = true
	b.timer.Stop()
	b.cancel()
	b.mu.Unlock()
}
