package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

// These tests use only in-memory providers and the existing fake keep-alive clock.
// Every channel holds a specific interleaving; no external service is contacted.
type criticalProvider struct {
	*kaProvider
	prewarm      func(context.Context, core.Request) (core.Usage, error)
	beforeStream func(context.Context, core.Request)
}

func (p *criticalProvider) Prewarm(ctx context.Context, req core.Request) (core.Usage, error) {
	return p.prewarm(ctx, req)
}

func (p *criticalProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	if p.beforeStream != nil {
		p.beforeStream(ctx, req)
	}
	return p.kaProvider.Stream(ctx, req)
}

func criticalReceive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for %s", what)
		var zero T
		return zero
	}
}

func criticalMoveClock(h *kaHarness, d time.Duration) {
	h.clock.mu.Lock()
	h.clock.now = h.clock.now.Add(d)
	h.clock.mu.Unlock()
}

func criticalInstall(t *testing.T, h *kaHarness, p *criticalProvider) {
	t.Helper()
	if err := h.agent.SetModel(p, kaModel); err != nil {
		t.Fatal(err)
	}
}

// RED: configuration setters do not cancel an already running obsolete ping.
func TestCriticalREDModelChangeCancelsActivePing(t *testing.T) {
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
	started := make(chan context.Context, 1)
	release, finished := make(chan struct{}), make(chan struct{})
	p := &criticalProvider{kaProvider: h.prov, prewarm: func(ctx context.Context, _ core.Request) (core.Usage, error) {
		started <- ctx
		<-release
		defer close(finished)
		return core.Usage{}, ctx.Err()
	}}
	criticalInstall(t, h, p)
	h.send("one")
	h.clock.advance(55*time.Minute, nil)
	pingCtx := criticalReceive(t, started, "active prewarm")
	defer func() { close(release); criticalReceive(t, finished, "prewarm exit") }()
	m := kaModel
	m.ID = "claude-sonnet-5-5"
	if err := h.agent.SetModel(nil, m); err != nil {
		t.Fatal(err)
	}
	if err := pingCtx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("obsolete ping remains live after SetModel returned: context error=%v", err)
	}
}

// RED: a config write can win after prefix validation and before admission.
func TestCriticalREDModelChangeBetweenValidationAndDispatch(t *testing.T) {
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
	pinged := make(chan core.Request, 1)
	p := &criticalProvider{kaProvider: h.prov, prewarm: func(_ context.Context, req core.Request) (core.Usage, error) {
		pinged <- req
		return core.Usage{CacheRead: 250_000}, nil
	}}
	criticalInstall(t, h, p)
	entered, release, evaluated := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var block atomic.Bool
	h.cfg.Policy = func() core.KeepAlivePolicy {
		if block.Load() {
			close(entered)
			<-release
		}
		return allRolesPolicy()
	}
	h.agent.SetKeepAlive(&h.cfg)
	h.send("one")
	block.Store(true)
	criticalMoveClock(h, 55*time.Minute)
	go func() { h.agent.keepAlive.Load().evaluate(); close(evaluated) }()
	criticalReceive(t, entered, "policy read after prefix validation")
	m := kaModel
	m.ID = "claude-sonnet-5-5"
	if err := h.agent.SetModel(nil, m); err != nil {
		t.Fatal(err)
	}
	close(release)
	criticalReceive(t, evaluated, "evaluation completion")
	select {
	case req := <-pinged:
		t.Fatalf("dispatched obsolete model %s after SetModel(%s) completed", req.Model.ID, m.ID)
	case <-time.After(100 * time.Millisecond):
	}
}

// RED: the TTL check uses the time read before a possible scheduling delay.
func TestCriticalREDTTLRecheckedAtDispatch(t *testing.T) {
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "")
	pinged := make(chan time.Time, 1)
	p := &criticalProvider{kaProvider: h.prov, prewarm: func(_ context.Context, _ core.Request) (core.Usage, error) {
		pinged <- h.clock.Now()
		return core.Usage{CacheWrite: 250_000}, nil
	}}
	criticalInstall(t, h, p)
	entered, release, evaluated := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h.cfg.PendingWork = func() bool { close(entered); <-release; return false }
	h.agent.SetKeepAlive(&h.cfg)
	h.send("one")
	origin := h.clock.Now()
	criticalMoveClock(h, 4*time.Minute+30*time.Second)
	go func() { h.agent.keepAlive.Load().evaluate(); close(evaluated) }()
	criticalReceive(t, entered, "state read after TTL validation")
	criticalMoveClock(h, 31*time.Second)
	close(release)
	criticalReceive(t, evaluated, "evaluation completion")
	select {
	case at := <-pinged:
		t.Fatalf("ping dispatched at age %s (TTL=5m0s, margin=30s): stale pre-delay timestamp admitted a cold write", at.Sub(origin))
	case <-time.After(100 * time.Millisecond):
	}
}

// RED: usage of an old ping is billable, but its successful completion must
// not become evidence that the new snapshot was kept warm.
func TestCriticalREDLateSuccessDoesNotContaminateNewGeneration(t *testing.T) {
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
	started, release := make(chan struct{}), make(chan struct{})
	p := &criticalProvider{kaProvider: h.prov, prewarm: func(_ context.Context, _ core.Request) (core.Usage, error) {
		close(started)
		<-release
		return core.Usage{CacheRead: 250_000}, nil
	}}
	criticalInstall(t, h, p)
	h.send("one")
	h.clock.advance(55*time.Minute, nil)
	criticalReceive(t, started, "prewarm")
	h.send("two")
	close(release)
	criticalReceive(t, h.evCh, "late ping success event")
	h.send("three")
	wakes := h.eventsOf(KeepAliveWake)
	if len(wakes) != 0 {
		t.Fatalf("third request falsely attributed %d old ping(s) to second snapshot; no ping of snapshot two was sent", wakes[0].Pings)
	}
}

// Control: nonblocking cancellation is intentional, but it is not a join.
// This records overlap without asserting that the real request should wait.
func TestCriticalControlRealRequestCancelsButDoesNotJoin(t *testing.T) {
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
	started, cancelSeen, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var active, overlap atomic.Bool
	p := &criticalProvider{kaProvider: h.prov}
	p.prewarm = func(ctx context.Context, _ core.Request) (core.Usage, error) {
		active.Store(true)
		close(started)
		<-ctx.Done()
		close(cancelSeen)
		<-release
		active.Store(false)
		close(finished)
		return core.Usage{}, ctx.Err()
	}
	p.beforeStream = func(_ context.Context, _ core.Request) {
		if active.Load() {
			overlap.Store(true)
		}
	}
	criticalInstall(t, h, p)
	h.send("one")
	h.clock.advance(55*time.Minute, nil)
	criticalReceive(t, started, "prewarm")
	h.send("two")
	criticalReceive(t, cancelSeen, "cancellation observation")
	close(release)
	criticalReceive(t, finished, "prewarm cleanup")
	if !overlap.Load() {
		t.Fatal("setup did not exercise delayed cancellation cleanup")
	}
	t.Log("real Stream entered while canceled Prewarm was still unwinding; cancellation was delivered, no join was performed")
}

func TestCriticalControlStopAndShutdownCancelActivePing(t *testing.T) {
	for _, operation := range []string{"stop", "shutdown"} {
		t.Run(operation, func(t *testing.T) {
			h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "1h")
			started, finished := make(chan context.Context, 1), make(chan struct{})
			p := &criticalProvider{kaProvider: h.prov, prewarm: func(ctx context.Context, _ core.Request) (core.Usage, error) {
				started <- ctx
				<-ctx.Done()
				close(finished)
				return core.Usage{}, ctx.Err()
			}}
			criticalInstall(t, h, p)
			h.send("one")
			h.clock.advance(55*time.Minute, nil)
			ctx := criticalReceive(t, started, "prewarm")
			if operation == "stop" {
				h.agent.Abort()
			} else {
				h.agent.SetKeepAlive(nil)
			}
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("ping context not canceled")
			}
			criticalReceive(t, finished, "prewarm cancellation")
		})
	}
}
