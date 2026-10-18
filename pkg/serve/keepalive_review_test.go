package serve

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/core"
)

type criticalServeProvider struct {
	*kaProvider
	prewarm func(context.Context, core.Request) (core.Usage, error)
}

func (p *criticalServeProvider) Prewarm(ctx context.Context, req core.Request) (core.Usage, error) {
	return p.prewarm(ctx, req)
}

func criticalReceive[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", what)
		var zero T
		return zero
	}
}

func criticalServePing(t *testing.T, h *kaServeHarness, fn func(context.Context, core.Request) (core.Usage, error)) <-chan agent.KeepAliveEvent {
	t.Helper()
	a := h.sess.runtime.Context().Agent.(*agent.Agent)
	model, ok := core.ResolveModel("claude-opus-5-5")
	if !ok {
		t.Fatal("missing model")
	}
	if err := a.SetModel(&criticalServeProvider{kaProvider: h.prov, prewarm: fn}, model); err != nil {
		t.Fatal(err)
	}
	cfg := h.mgr.keepAliveConfig(h.sess, core.KeepAliveRoleSession, "")
	events := make(chan agent.KeepAliveEvent, 8)
	onEvent := cfg.OnEvent
	cfg.OnEvent = func(e agent.KeepAliveEvent) { onEvent(e); events <- e }
	a.SetKeepAlive(cfg)
	sendAndWaitIdle(t, h.mgr, h.sess, "snapshot for the gated fake", "")
	h.clock.advance(55*time.Minute, nil)
	return events
}

// RED: known successful usage arriving after close/shutdown never reaches the
// session's cost reactor, even though telemetry records the same usage.
func TestCriticalREDLatePingCostSurvivesLifecycleTeardown(t *testing.T) {
	for _, operation := range []string{"close", "shutdown"} {
		t.Run(operation, func(t *testing.T) {
			h := newKAServe(t, allKA())
			started := make(chan context.Context, 1)
			release := make(chan struct{})
			events := criticalServePing(t, h, func(ctx context.Context, _ core.Request) (core.Usage, error) {
				started <- ctx
				<-release
				return core.Usage{CacheRead: kaContextTokens}, nil
			})
			ctx := criticalReceive(t, started, "prewarm")
			before := sessionCostOf(t, h.sess)
			if operation == "close" {
				if err := h.mgr.CloseSession(h.sess.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				h.mgr.Shutdown()
			}
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("teardown failed to cancel prewarm")
			}
			close(release)
			e := criticalReceive(t, events, "late successful usage after teardown")
			if e.Kind != agent.KeepAlivePing || e.CostUSD <= 0 {
				t.Fatalf("setup: event=%+v", e)
			}
			saved, ok := diskSessionCost(t, h.mgr, h.sess.ID)
			after, number := saved.(float64)
			if !ok || !number {
				t.Fatalf("setup: no persisted session cost: %#v", saved)
			}
			if math.Abs((after-before)-e.CostUSD) > 1e-9 {
				t.Fatalf("%s returned before ping settled: known usage cost=$%.6f, recorded session delta=$%.6f; ping trace lines=%d", operation, e.CostUSD, after-before, h.kindCount("ping"))
			}
		})
	}
}

// Control: close and delete do deliver cancellation, and a cooperatively
// canceled ping cannot schedule a fresh ping later.
func TestCriticalControlCloseDeleteCancelAndDoNotRearm(t *testing.T) {
	for _, operation := range []string{"close", "delete"} {
		t.Run(operation, func(t *testing.T) {
			h := newKAServe(t, allKA())
			started, finished := make(chan context.Context, 1), make(chan struct{})
			events := criticalServePing(t, h, func(ctx context.Context, _ core.Request) (core.Usage, error) {
				started <- ctx
				<-ctx.Done()
				close(finished)
				return core.Usage{}, ctx.Err()
			})
			ctx := criticalReceive(t, started, "prewarm")
			var err error
			if operation == "close" {
				err = h.mgr.CloseSession(h.sess.ID)
			} else {
				err = h.mgr.Delete(h.sess.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("context not canceled")
			}
			criticalReceive(t, finished, "prewarm cancellation")
			h.advance(3 * time.Hour)
			select {
			case e := <-events:
				t.Fatalf("unexpected event after canceled teardown: %+v", e)
			default:
			}
		})
	}
}

// RED: Settings promises immediate disable, but a live prewarm is untouched.
func TestCriticalREDSettingsOffCancelsActivePing(t *testing.T) {
	h := newKAServe(t, allKA())
	started, release, exited := make(chan context.Context, 1), make(chan struct{}), make(chan struct{})
	criticalServePing(t, h, func(ctx context.Context, _ core.Request) (core.Usage, error) {
		started <- ctx
		<-release
		close(exited)
		return core.Usage{}, ctx.Err()
	})
	ctx := criticalReceive(t, started, "prewarm")
	defer func() { close(release); criticalReceive(t, exited, "prewarm exit") }()
	req := httptest.NewRequest(http.MethodPatch, "/api/cache-keepalive", strings.NewReader(`{"owner":false,"session":false,"subagent":false}`))
	w := httptest.NewRecorder()
	handleCacheKeepAlivePatch(w, req, h.mgr)
	if w.Code != http.StatusOK {
		t.Fatalf("patch failed: %d %s", w.Code, w.Body.String())
	}
	if h.mgr.keepAlive.policy().RoleEnabled(core.KeepAliveRoleSession) {
		t.Fatal("setup: role still on")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("Settings PATCH returned 200 with session=false, but active ping context=%v (not canceled)", ctx.Err())
	}
}

// RED: a scheduler evaluation paused after reading the old policy can start
// a brand new ping after the OFF operation has already returned.
func TestCriticalREDSettingsOffWinsPausedEvaluation(t *testing.T) {
	h := newKAServe(t, allKA())
	a := h.sess.runtime.Context().Agent.(*agent.Agent)
	pinged := make(chan struct{}, 1)
	model, _ := core.ResolveModel("claude-opus-5-5")
	if err := a.SetModel(&criticalServeProvider{kaProvider: h.prov, prewarm: func(_ context.Context, _ core.Request) (core.Usage, error) {
		pinged <- struct{}{}
		return core.Usage{CacheRead: kaContextTokens}, nil
	}}, model); err != nil {
		t.Fatal(err)
	}
	cfg := h.mgr.keepAliveConfig(h.sess, core.KeepAliveRoleSession, "")
	entered, release, evaluated := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var pause atomic.Bool
	policy := cfg.Policy
	cfg.Policy = func() core.KeepAlivePolicy {
		p := policy()
		if pause.Load() {
			close(entered)
			<-release
		}
		return p
	}
	a.SetKeepAlive(cfg)
	sendAndWaitIdle(t, h.mgr, h.sess, "snapshot before OFF", "")
	pause.Store(true)
	go func() { h.clock.advance(55*time.Minute, nil); close(evaluated) }()
	criticalReceive(t, entered, "paused policy evaluation")
	h.mgr.keepAlive.set(nil)
	close(release)
	criticalReceive(t, evaluated, "evaluation exit")
	select {
	case <-pinged:
		t.Fatal("new Prewarm dispatched after Settings OFF completed; evaluation used an obsolete policy")
	case <-time.After(100 * time.Millisecond):
	}
}
