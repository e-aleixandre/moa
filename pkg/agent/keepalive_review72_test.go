package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/anthropic"
)

type reviewWireArrival struct {
	at        time.Time
	wall      time.Time
	maxTokens int
	stream    bool
}
type reviewRoundTrip func(*http.Request) (*http.Response, error)

func (f reviewRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type reviewWireProvider struct {
	*kaProvider
	real     *anthropic.Anthropic
	finished chan error
}

func (p *reviewWireProvider) Prewarm(ctx context.Context, req core.Request) (core.Usage, error) {
	u, err := p.real.Prewarm(ctx, req)
	p.finished <- err
	return u, err
}

// Only the model's ordinary response is fake. Every ping uses production
// Anthropic.Prewarm, its body builder, and net/http to a loopback server.
func reviewWireSetup(t *testing.T, phase string) (*kaHarness, <-chan context.Context, func(), <-chan reviewWireArrival, <-chan error) {
	t.Helper()
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "")
	arrived := make(chan reviewWireArrival, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read ping body: %v", err)
		}
		var body struct {
			MaxTokens int  `json:"max_tokens"`
			Stream    bool `json:"stream"`
		}
		if err := json.Unmarshal(data, &body); err != nil {
			t.Errorf("parse ping body: %v", err)
		}
		arrived <- reviewWireArrival{at: h.clock.Now(), wall: time.Now(), maxTokens: body.MaxTokens, stream: body.Stream}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":250000}}`)
	}))
	t.Cleanup(server.Close)
	entered, gate := make(chan context.Context, 1), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	real := anthropic.NewWithBaseURL("sk-ant-oat-review-not-a-real-token", server.URL)
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	switch phase {
	case "auth":
		real.WithDispatch(func(ctx context.Context) (string, error) {
			entered <- ctx
			<-gate
			return "sk-ant-oat-review-not-a-real-token", nil
		})
		real.WithHTTPClient(&http.Client{Transport: transport})
	case "transport":
		real.WithHTTPClient(&http.Client{Transport: reviewRoundTrip(func(r *http.Request) (*http.Response, error) {
			entered <- r.Context()
			<-gate
			return transport.RoundTrip(r)
		})})
	default:
		t.Fatalf("unknown delay phase %q", phase)
	}
	finished := make(chan error, 1)
	p := &reviewWireProvider{kaProvider: h.prov, real: real, finished: finished}
	if err := h.agent.SetModel(p, kaModel); err != nil {
		t.Fatal(err)
	}
	return h, entered, release, arrived, finished
}

func TestReview72REDTTLAtActualHTTPDispatch(t *testing.T) {
	for _, phase := range []string{"auth", "transport"} {
		t.Run(phase, func(t *testing.T) {
			h, entered, release, arrived, finished := reviewWireSetup(t, phase)
			h.send("snapshot")
			origin := h.clock.Now()
			h.clock.advance(4*time.Minute+30*time.Second, nil)
			ctx := criticalReceive(t, entered, "delayed prewarm dispatch")
			if phase == "transport" {
				// Past the dispatch gate nothing reads the keep-alive clock:
				// the bound inside the transport is the context's wall-clock
				// deadline, TTL - margin from the admission (exercised in real
				// time by TestReview72REDTTLWallClockDuringAuthDispatch). A
				// fake clock moved without time passing cannot trip it, so
				// here the deadline itself is asserted.
				dl, ok := ctx.Deadline()
				if left := time.Until(dl); !ok || left > 15*time.Second || left <= 0 {
					t.Fatalf("transport context deadline in %v (ok=%v), want within the 15s left before TTL - margin", left, ok)
				}
				release()
				_ = criticalReceive(t, finished, "HTTP prewarm completion")
				return
			}
			criticalMoveClock(h, 31*time.Second)
			if ctx.Err() != nil {
				t.Fatalf("setup: context unexpectedly cancelled: %v", ctx.Err())
			}
			release()
			err := criticalReceive(t, finished, "HTTP prewarm completion")
			select {
			case wire := <-arrived:
				if wire.maxTokens != 0 || wire.stream {
					t.Fatalf("not a prewarm: %+v", wire)
				}
				t.Fatalf("NEW Anthropic HTTP POST reached loopback at age %s (TTL 5m; required margin 15s); delay=%s, Prewarm error=%v", wire.at.Sub(origin), phase, err)
			default:
				t.Logf("expired ping was not sent: %v", err)
			}
		})
	}
}

func TestReview72ControlDelayedDispatchCancellation(t *testing.T) {
	for _, phase := range []string{"auth", "transport"} {
		for _, operation := range []string{"off", "model", "thinking", "abort", "shutdown"} {
			t.Run(phase+"/"+operation, func(t *testing.T) {
				h, entered, release, arrived, finished := reviewWireSetup(t, phase)
				var group KeepAliveGroup
				h.cfg.Group = &group
				h.agent.SetKeepAlive(&h.cfg)
				h.send("snapshot")
				h.clock.advance(4*time.Minute+30*time.Second, nil)
				ctx := criticalReceive(t, entered, "prewarm before wire")
				switch operation {
				case "off":
					group.Stop(func(string) bool { return true })
				case "model":
					m := kaModel
					m.ID = "claude-sonnet-5-5"
					if err := h.agent.SetModel(nil, m); err != nil {
						t.Fatal(err)
					}
				case "thinking":
					if err := h.agent.SetThinkingLevel("low"); err != nil {
						t.Fatal(err)
					}
				case "abort":
					h.agent.Abort()
				case "shutdown":
					h.agent.SetKeepAlive(nil)
				}
				if ctx.Err() != context.Canceled {
					t.Errorf("%s returned without canceling pending send: %v", operation, ctx.Err())
				}
				release()
				err := criticalReceive(t, finished, "canceled prewarm completion")
				if err == nil {
					t.Error("expected cancellation error")
				}
				select {
				case at := <-arrived:
					t.Fatalf("NEW HTTP POST after %s: %+v", operation, at)
				default:
				}
			})
		}
	}
}

func TestReview72REDOtherPrefixSettersCancelDelayedDispatch(t *testing.T) {
	for _, operation := range []string{"fast", "system", "reset", "load-state", "trim"} {
		t.Run(operation, func(t *testing.T) {
			h, entered, release, arrived, finished := reviewWireSetup(t, "auth")
			h.send("snapshot")
			h.clock.advance(4*time.Minute+30*time.Second, nil)
			ctx := criticalReceive(t, entered, "prewarm before prefix change")
			var err error
			switch operation {
			case "fast":
				h.agent.SetFast(true)
			case "system":
				err = h.agent.SetSystemPrompt("changed system")
			case "reset":
				err = h.agent.Reset()
			case "load-state":
				err = h.agent.LoadState(nil, 1)
			case "trim":
				err = h.agent.SetTrimWatermark("changed-trim-mark")
			}
			if err != nil {
				t.Fatalf("setter failed: %v", err)
			}
			ctxErrAtSetter := ctx.Err()
			release()
			err = criticalReceive(t, finished, "prewarm after prefix change")
			select {
			case at := <-arrived:
				t.Fatalf("NEW HTTP POST for obsolete prefix after %s returned (ctx=%v, err=%v, at=%s)", operation, ctxErrAtSetter, err, at.at)
			default:
			}
		})
	}
}

// Callbacks may enter agent setters, settings, and agent shutdown. They must
// not run under either admission lock; invalidations must beat stale reads.
func TestReview72ControlCallbacksAreOutsideAdmissionLocks(t *testing.T) {
	for _, which := range []string{"policy", "pending", "exempt", "event"} {
		t.Run(which, func(t *testing.T) {
			h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "")
			var group KeepAliveGroup
			h.cfg.Group = &group
			var active atomic.Bool
			called := make(chan struct{}, 1)
			callback := func() {
				if !active.Load() {
					return
				}
				if err := h.agent.SetThinkingLevel("low"); err != nil {
					panic(err)
				}
				group.Stop(func(string) bool { return true })
				_ = h.agent.Model()
				called <- struct{}{}
			}
			switch which {
			case "policy":
				h.cfg.Policy = func() core.KeepAlivePolicy { callback(); return allRolesPolicy() }
			case "pending":
				h.cfg.PendingWork = func() bool { callback(); return false }
			case "exempt":
				h.cfg.Exempt = func() bool { callback(); return false }
			case "event":
				h.cfg.OnEvent = func(KeepAliveEvent) { callback() }
			}
			h.agent.SetKeepAlive(&h.cfg)
			h.send("snapshot")
			active.Store(true)
			done := make(chan struct{})
			go func() { h.clock.advance(4*time.Minute+30*time.Second, nil); close(done) }()
			criticalReceive(t, done, "evaluation (lock order)")
			criticalReceive(t, called, fmt.Sprintf("reentrant %s callback", which))
		})
	}
}

func TestReview72ControlGroupAndSettersDuringExternalCallbacks(t *testing.T) {
	for _, which := range []string{"pending", "exempt"} {
		t.Run(which, func(t *testing.T) {
			var group KeepAliveGroup
			h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "")
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			callback := func() bool { close(entered); <-release; return false }
			h.cfg.Group = &group
			if which == "pending" {
				h.cfg.PendingWork = callback
			} else {
				h.cfg.Exempt = callback
			}
			h.agent.SetKeepAlive(&h.cfg)
			h.send("snapshot")
			ka := h.agent.keepAlive.Load()
			criticalMoveClock(h, 4*time.Minute+30*time.Second)
			evaluated := make(chan struct{})
			go func() { ka.evaluate(); close(evaluated) }()
			criticalReceive(t, entered, "external callback")
			mutated := make(chan struct{})
			go func() {
				group.Stop(func(role string) bool { return role == core.KeepAliveRoleOwner })
				_ = h.agent.SetModel(nil, kaModel)
				_ = h.agent.SetThinkingLevel("low")
				h.agent.SetFast(true)
				h.agent.SetKeepAlive(nil)
				close(mutated)
			}()
			criticalReceive(t, mutated, "settings Stop + setters + shutdown while callback blocked")
			unblock()
			criticalReceive(t, evaluated, "stale evaluation exit")
			if n := h.prov.pingCount(); n != 0 {
				t.Fatalf("stale evaluation dispatched %d ping(s)", n)
			}
			group.mu.Lock()
			members := len(group.agents)
			group.mu.Unlock()
			if members != 0 {
				t.Fatalf("group retained %d shutdown keep-alives", members)
			}
		})
	}
}

func TestReview72ControlConcurrentGroupReplacementAndSetters(t *testing.T) {
	var group KeepAliveGroup
	h := newKAHarness(t, core.KeepAliveRoleOwner, 250_000, "")
	h.cfg.Group = &group
	h.agent.SetKeepAlive(&h.cfg)
	h.send("snapshot")
	criticalMoveClock(h, 4*time.Minute+30*time.Second)
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				switch worker {
				case 0:
					group.Stop(func(string) bool { return true })
				case 1:
					_ = h.agent.SetModel(nil, kaModel)
					_ = h.agent.SetThinkingLevel("high")
				case 2:
					if ka := h.agent.keepAlive.Load(); ka != nil {
						ka.evaluate()
					}
				case 3:
					h.agent.SetKeepAlive(&h.cfg)
				}
			}
		}(worker)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	criticalReceive(t, done, "concurrent group/evaluation/setter/replacement completion")
	h.agent.SetKeepAlive(nil)
	group.mu.Lock()
	members := len(group.agents)
	group.mu.Unlock()
	if members != 0 {
		t.Fatalf("group retained %d removed agents", members)
	}
}

// The accelerated fake-clock reproduction above is complemented by an actual
// wall-clock wait: advancing a fake clock does not expire context timers. Only
// the fixture's initial cache age is backdated, to avoid a five-minute setup.
func TestReview72REDTTLWallClockDuringAuthDispatch(t *testing.T) {
	h, entered, release, arrived, finished := reviewWireSetup(t, "auth")
	h.cfg.Clock = systemKeepAliveClock{}
	h.agent.SetKeepAlive(&h.cfg)
	h.send("snapshot")
	ka := h.agent.keepAlive.Load()
	ka.mu.Lock()
	ka.stopTimerLocked()
	ka.snap.anchor = ka.snap.anchor.Add(-4*time.Minute - 44*time.Second)
	ka.snap.sentAt = ka.snap.anchor
	origin := ka.snap.anchor
	ka.mu.Unlock()
	ka.evaluate()
	ctx := criticalReceive(t, entered, "wall-clock auth dispatch")
	deadline, _ := ctx.Deadline()
	t.Logf("at auth gate: cache remaining=%s; context remaining=%s", time.Until(origin.Add(5*time.Minute)), time.Until(deadline))
	// TTL remaining is 16s at admission. Hold a real 17s, still well within
	// today's unconditional 2-minute ping timeout, but beyond the cache TTL.
	timer := time.AfterFunc(17*time.Second, release)
	defer timer.Stop()
	var err error
	select {
	case err = <-finished:
	case <-time.After(23 * time.Second):
		t.Fatal("prewarm did not finish after real 17s auth delay")
	}
	select {
	case at := <-arrived:
		t.Fatalf("NEW HTTP POST at wall-clock cache age %s after real 17s auth delay (TTL 5m, margin 15s, err=%v)", at.wall.Sub(origin), err)
	default:
		t.Logf("expired ping prevented at the HTTP boundary: %v", err)
	}
}
