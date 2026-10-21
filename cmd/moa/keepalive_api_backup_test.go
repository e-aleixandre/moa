package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

type apiPrewarmFixture struct {
	f     *runtimeFixture
	p     *snapshotProvider
	wire  *backupWire
	req   core.Request
	last  core.Message
	reset time.Time
}

func newAPIPrewarmFixture(t *testing.T, reliableReset bool) *apiPrewarmFixture {
	t.Helper()
	f := newRuntimeFixture(t)
	t.Cleanup(func() {
		calls, tokens := f.u.snapshot()
		if len(calls) != 0 || len(tokens) != 0 {
			t.Errorf("prewarm escaped its fake inference wire or refreshed OAuth: calls=%d token exchanges=%d", len(calls), len(tokens))
		}
	})
	gen := f.commit(t, f.store, "anthropic", auth.Credential{Type: "oauth", Access: "fake-oauth-access", Refresh: "fake-refresh", Expires: 4102444800000})
	if _, err := f.store.SaveAnthropicAPIKey(gen, "fake-api-backup"); err != nil {
		t.Fatal(err)
	}
	model, ok := core.ResolveModel("claude-opus-5-5")
	if !ok {
		t.Fatal("missing test model")
	}
	h := &apiPrewarmFixture{f: f, wire: &backupWire{}, reset: time.Now().Add(2 * time.Hour).Truncate(time.Second)}
	h.p = &snapshotProvider{model: model, authStore: f.store, httpClient: &http.Client{Transport: h.wire}}
	h.req = nativeIdentityRequest()
	h.req.Model = model
	h.req.Options.OnProviderRetry = func(_ context.Context, w core.ProviderWait) error {
		return &core.ProviderRetryReady{Attempt: w.Attempt, Wait: &w}
	}
	h.wire.serve = func(_ *http.Request, n int) (int, http.Header, string) {
		if n == 0 {
			if reliableReset {
				return backupRejectUntil("seven_day", h.reset)
			}
			return backupReject("five_hour")
		}
		return backupOK("end_turn")
	}
	h.last = finishBackup(t, h.p, context.Background(), h.req)
	if h.last.ProviderSource == nil || h.last.ProviderSource.Kind != "api_backup" || len(h.wire.headers) != 2 {
		t.Fatalf("fixture did not produce an API response: source=%+v calls=%d", h.last.ProviderSource, len(h.wire.headers))
	}
	return h
}

func (h *apiPrewarmFixture) ping() core.Request {
	req := h.req
	req.Options.ProviderBinding = h.last.ProviderSource
	return req
}

func assertAPIPrewarmBody(t *testing.T, realBody, pingBody []byte) {
	t.Helper()
	var real, ping map[string]json.RawMessage
	if err := json.Unmarshal(realBody, &real); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(pingBody, &ping); err != nil {
		t.Fatal(err)
	}
	if string(ping["stream"]) != "false" || string(ping["max_tokens"]) != "0" {
		t.Fatalf("ping stream=%s max_tokens=%s", ping["stream"], ping["max_tokens"])
	}
	delete(real, "stream")
	delete(real, "max_tokens")
	delete(ping, "stream")
	delete(ping, "max_tokens")
	if !reflect.DeepEqual(real, ping) {
		t.Errorf("prewarm changed the API cached request outside stream/max_tokens:\nreal %s\nping %s", realBody, pingBody)
	}
	var system []struct{ Text string }
	if err := json.Unmarshal(ping["system"], &system); err != nil {
		t.Fatal(err)
	}
	if len(system) != 1 || system[0].Text != "Synthetic system: "+nativeIdentityPreamble {
		t.Errorf("generated Code identity present or legitimate system altered: %s", ping["system"])
	}
	if !bytes.Contains(ping["messages"], []byte(nativeIdentityPreamble)) {
		t.Error("legitimate user/history phrase was removed")
	}
}

func TestAPIBackupPrewarmExactOriginAndNativePrefix(t *testing.T) {
	for _, reliable := range []bool{true, false} {
		name := "remembered_reset"
		if !reliable {
			name = "no_reliable_reset"
		}
		t.Run(name, func(t *testing.T) {
			h := newAPIPrewarmFixture(t, reliable)
			h.wire.serve = prewarmOK
			// A retry date without a reliable reset has passed by any useful ping.
			h.p.now = func() time.Time { return h.reset.Add(-time.Hour) }
			req := h.ping()
			req.Options.APIKey = "fake-unrelated-caller-key"
			seenBoundSource := false
			req.Options.OnProviderDispatch = func(_ context.Context, src core.ProviderSource) error {
				if src.Kind == "api_backup" {
					seenBoundSource = src.PrimaryGeneration == h.last.ProviderSource.PrimaryGeneration && src.BackupGeneration == h.last.ProviderSource.BackupGeneration && src.Model == h.req.Model.ID && src.Provider == "anthropic"
				}
				return nil
			}
			usage, err := h.p.Prewarm(context.Background(), req)
			if err != nil {
				t.Fatalf("eligible API backup prewarm refused: %v", err)
			}
			if len(h.wire.headers) != 3 || !seenBoundSource {
				t.Fatalf("calls=%d gate saw exact binding=%v", len(h.wire.headers), seenBoundSource)
			}
			if usage.CacheRead != 250000 || usage.Input != 3 || usage.Output != 0 {
				t.Errorf("usage=%+v", usage)
			}
			assertNativeAPIHeaders(t, h.wire.headers[2])
			if !reflect.DeepEqual(h.wire.headers[1], h.wire.headers[2]) {
				t.Error("API prewarm headers differ from preceding real API request")
			}
			assertAPIPrewarmBody(t, h.wire.bodies[1], h.wire.bodies[2])
		})
	}
}

func TestAPIBackupPrewarmRejectsChangedAuthorization(t *testing.T) {
	for _, change := range []string{"key", "oauth", "remove", "env", "binding_model", "binding_provider", "binding_profile", "fast", "pinned_voice", "stop", "gate"} {
		t.Run(change, func(t *testing.T) {
			h := newAPIPrewarmFixture(t, true)
			req := h.ping()
			b := *req.Options.ProviderBinding
			req.Options.ProviderBinding = &b
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var err error
			switch change {
			case "key":
				_, err = h.f.other.SaveAnthropicAPIKey(b.PrimaryGeneration, "fake-replacement-key")
			case "oauth":
				_, err = h.f.other.CommitLogin("anthropic", b.PrimaryGeneration, auth.Credential{Type: "oauth", Access: "fake-new-oauth", Refresh: "fake-new-refresh", Expires: 4102444800000})
			case "remove":
				err = h.f.other.RemoveAnthropicAPIKey(b.BackupGeneration)
			case "env":
				t.Setenv("ANTHROPIC_API_KEY", "fake-env-key")
			case "binding_model":
				b.Model = "claude-sonnet-5-5"
			case "binding_provider":
				b.Provider = "openai"
			case "binding_profile":
				b.WireProfile = ""
			case "fast":
				req.Options.Fast = true
			case "pinned_voice":
				snap, e := h.f.store.PeekSnapshot("anthropic")
				err, h.p.plan = e, &snap
			case "stop":
				cancel()
			case "gate":
				req.Options.OnProviderDispatch = func(context.Context, core.ProviderSource) error { return core.ErrProviderReconfigured }
			}
			if err != nil {
				t.Fatal(err)
			}
			h.wire.serve = prewarmOK
			if _, err := h.p.Prewarm(ctx, req); err == nil || len(h.wire.headers) != 2 {
				t.Fatalf("%s allowed a ping: err=%v calls=%d", change, err, len(h.wire.headers))
			}
		})
	}
}

func TestAPIBackupPrewarmLastCreditFailureAcrossSessions(t *testing.T) {
	h := newAPIPrewarmFixture(t, true)
	h.wire.serve = func(*http.Request, int) (int, http.Header, string) {
		return 400, http.Header{}, `{"error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`
	}
	other := &snapshotProvider{model: h.p.model, authStore: h.f.store, httpClient: h.p.httpClient}
	if _, err := other.Stream(context.Background(), h.req); err == nil || !strings.Contains(err.Error(), "credit balance") || len(h.wire.headers) != 3 {
		t.Fatalf("foreground denial not visible exactly once: err=%v calls=%d", err, len(h.wire.headers))
	}
	h.wire.serve = prewarmOK
	if _, err := h.p.Prewarm(context.Background(), h.ping()); err == nil || len(h.wire.headers) != 3 {
		t.Fatalf("known depleted same-generation cache was pinged: err=%v calls=%d", err, len(h.wire.headers))
	}
	if snap, err := h.f.store.PeekSnapshot("anthropic"); err != nil || snap.Kind != "oauth" {
		t.Fatalf("credit failure invalidated OAuth: %v", err)
	}
}

func TestAPIBackupPrewarmGenericFailureIsNotCreditExhaustion(t *testing.T) {
	for _, status := range []int{400, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := newAPIPrewarmFixture(t, true)
			// Record the error from another real request. The retry callback
			// returns immediately; no retry or real network is involved.
			h.wire.serve = func(*http.Request, int) (int, http.Header, string) {
				return status, http.Header{}, `{"error":{"type":"invalid_request_error","message":"unrelated synthetic upstream failure"}}`
			}
			if _, err := h.p.Stream(context.Background(), h.req); err == nil || len(h.wire.headers) != 3 {
				t.Fatalf("fixture error=%v calls=%d", err, len(h.wire.headers))
			}
			h.wire.serve = prewarmOK
			if _, err := h.p.Prewarm(context.Background(), h.ping()); err != nil || len(h.wire.headers) != 4 {
				t.Fatalf("non-credit error incorrectly prevented prewarm: err=%v calls=%d", err, len(h.wire.headers))
			}
		})
	}
}

func TestAPIBackupPrewarmResetAlreadyExpiredDoesNotProbeOAuth(t *testing.T) {
	h := newAPIPrewarmFixture(t, true)
	h.p.now = func() time.Time { return h.reset }
	h.wire.serve = prewarmOK
	if _, err := h.p.Prewarm(context.Background(), h.ping()); err == nil || len(h.wire.headers) != 2 {
		t.Fatalf("expired reset ping/probe: err=%v calls=%d", err, len(h.wire.headers))
	}
}

type apiPrewarmProbe struct {
	*snapshotProvider
	attempts int
	ready    <-chan struct{}
}

func (p *apiPrewarmProbe) Prewarm(ctx context.Context, req core.Request) (core.Usage, error) {
	if p.ready != nil {
		select {
		case <-p.ready:
		case <-ctx.Done():
			return core.Usage{}, ctx.Err()
		}
	}
	p.attempts++
	return p.snapshotProvider.Prewarm(ctx, req)
}

func TestAPIBackupPrewarmAgentTimerLifecycle(t *testing.T) {
	for _, action := range []string{"success", "success_no_reliable_reset", "ping_low_credit", "stop", "real_turn_low_credit"} {
		t.Run(action, func(t *testing.T) {
			h := newAPIPrewarmFixture(t, action != "success_no_reliable_reset")
			synctest.Test(t, func(t *testing.T) {
				// The fixture's reliable date was created outside the fake-time
				// bubble; leave it future for this lifecycle test.
				h.p.now = func() time.Time { return h.reset.Add(-time.Hour) }
				a, _, _ := newBackupAgent(t, h.p, core.NewRegistry())
				ready := make(chan struct{})
				probe := &apiPrewarmProbe{snapshotProvider: h.p, ready: ready}
				if err := a.SetModel(probe, h.p.model); err != nil {
					t.Fatal(err)
				}
				events := make(chan agent.KeepAliveEvent, 8)
				policy, enabled := core.GetCacheKeepAlive(core.MoaConfig{CacheKeepAlive: &core.CacheKeepAliveConfig{Owner: &core.KeepAliveHorizons{Enabled: true, MinContextTokens: new(0)}}})
				if !enabled {
					t.Fatal("test keep-alive policy disabled")
				}
				a.SetKeepAlive(&agent.KeepAliveConfig{Role: core.KeepAliveRoleOwner, Policy: func() core.KeepAlivePolicy { return policy }, OnEvent: func(e agent.KeepAliveEvent) { events <- e }})
				defer a.SetKeepAlive(nil)
				h.wire.serve = func(r *http.Request, _ int) (int, http.Header, string) {
					if r.Header.Get("Authorization") != "" {
						return backupReject("five_hour")
					}
					return backupOK("end_turn")
				}
				if _, err := a.Send(context.Background(), "real cached turn"); err != nil {
					t.Fatal(err)
				}
				before := len(h.wire.headers)
				h.wire.serve = prewarmOK
				switch action {
				case "stop":
					a.Abort()
				case "ping_low_credit", "real_turn_low_credit":
					h.wire.serve = func(*http.Request, int) (int, http.Header, string) {
						return 400, http.Header{}, `{"error":{"type":"invalid_request_error","message":"Your credit balance is too low"}}`
					}
					if action == "real_turn_low_credit" {
						if _, err := a.Send(context.Background(), "fails credit"); err == nil || !strings.Contains(err.Error(), "credit") {
							t.Fatalf("expected foreground credit error, got %v", err)
						}
						before = len(h.wire.headers)
					}
				}
				close(ready)
				time.Sleep(4*time.Minute + 30*time.Second)
				synctest.Wait()
				want := 0
				if action == "success" || action == "success_no_reliable_reset" || action == "ping_low_credit" {
					want = 1
				}
				if probe.attempts != want {
					t.Fatalf("timer Prewarm invocations=%d want=%d (action=%s)", probe.attempts, want, action)
				}
				if got := len(h.wire.headers) - before; got != want {
					t.Fatalf("due API pings=%d want=%d (action=%s)", got, want, action)
				}
				if want == 0 {
					time.Sleep(30 * time.Minute)
					synctest.Wait()
					if len(h.wire.headers) != before || probe.attempts != 0 {
						t.Fatal("stopped/failed real turn rearmed keep-alive")
					}
					return
				}
				select {
				case e := <-events:
					kind := agent.KeepAlivePing
					if action == "ping_low_credit" {
						kind = agent.KeepAlivePingError
					}
					if e.Kind != kind {
						t.Fatalf("event kind=%s want=%s", e.Kind, kind)
					}
					if action != "ping_low_credit" && (e.Usage.CacheRead != 250000 || e.CostUSD <= 0) {
						t.Fatalf("lost actual ping accounting: %+v", e)
					}
				default:
					t.Fatal("no keep-alive event after settled timer")
				}
				if action == "ping_low_credit" {
					time.Sleep(30 * time.Minute)
					synctest.Wait()
					if len(h.wire.headers) != before+1 || probe.attempts != 1 {
						t.Fatal("failed ping retried/rearmed")
					}
				}
			})
		})
	}
}

func TestAPIBackupPrewarmGateErrorIsPreserved(t *testing.T) {
	h := newAPIPrewarmFixture(t, true)
	gateErr := errors.New("synthetic keep-alive fence rejection")
	req := h.ping()
	req.Options.OnProviderDispatch = func(context.Context, core.ProviderSource) error { return gateErr }
	_, err := h.p.Prewarm(context.Background(), req)
	if !errors.Is(err, gateErr) || len(h.wire.headers) != 2 {
		t.Fatalf("dispatch gate was bypassed/not reached: err=%v calls=%d", err, len(h.wire.headers))
	}
}
