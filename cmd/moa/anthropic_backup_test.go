package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

type backupWire struct {
	headers []http.Header
	bodies  [][]byte
	serve   func(*http.Request, int) (int, http.Header, string)
}

func (tr *backupWire) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != "https://api.anthropic.com/v1/messages" || r.Method != "POST" {
		return nil, errors.New("network forbidden")
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	tr.bodies = append(tr.bodies, body)
	tr.headers = append(tr.headers, r.Header.Clone())
	status, h, text := tr.serve(r, len(tr.headers)-1)
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(text)), Request: r}, nil
}
func backupReject(scope string) (int, http.Header, string) {
	h := http.Header{}
	for k, v := range map[string]string{"status": "rejected", "representative-claim": scope, "overage-status": "rejected"} {
		h.Set("anthropic-ratelimit-unified-"+k, v)
	}
	return 429, h, `{"error":{"type":"rate_limit_error"}}`
}
func backupOK(stop string) (int, http.Header, string) {
	return 200, http.Header{}, "event: message_start\ndata: {\"message\":{\"model\":\"claude-opus-5-5\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {}\n\nevent: message_delta\ndata: {\"delta\":{\"stop_reason\":\"" + stop + "\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {}\n\n"
}
func backupRuntime(t *testing.T) (*snapshotProvider, *auth.Store, *backupWire, core.Request) {
	t.Helper()
	f := newRuntimeFixture(t)
	gen := f.commit(t, f.store, "anthropic", auth.Credential{Type: "oauth", Access: "fake-oauth-access", Refresh: "fake-refresh", Expires: 4102444800000})
	// Seed the planned dual schema so the pre-implementation RED exercises
	// runtime routing, rather than stopping at the missing management method.
	data, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	var disk map[string]json.RawMessage
	_ = json.Unmarshal(data, &disk)
	disk["__anthropic_backup_v1"] = json.RawMessage(`{"type":"api_key","key":"fake-api-backup","generation":"fake-key-generation","backup_policy":{"version":1,"primary_generation":"` + gen + `","policy_generation":"fake-policy-generation","enabled":true}}`)
	data, _ = json.Marshal(disk)
	if err := os.WriteFile(f.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	tr := &backupWire{}
	p := &snapshotProvider{model: core.Model{ID: "claude-opus-5-5", Provider: "anthropic"}, authStore: f.store, httpClient: &http.Client{Transport: tr}}
	req := core.Request{Model: p.model, System: "unchanged system", Messages: []core.Message{core.NewUserMessage("offline")}, Options: core.StreamOptions{OnProviderRetry: func(_ context.Context, w core.ProviderWait) error {
		return &core.ProviderRetryReady{Attempt: w.Attempt, Wait: &w}
	}}}
	return p, f.store, tr, req
}
func finishBackup(t *testing.T, p *snapshotProvider, ctx context.Context, req core.Request) core.Message {
	t.Helper()
	ch, err := p.Stream(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	var msg core.Message
	for ev := range ch {
		if ev.Error != nil {
			t.Fatal(ev.Error)
		}
		if ev.Message != nil {
			msg = *ev.Message
		}
	}
	return msg
}
func TestBackupRuntimeFiveSevenAndNaturalRestore(t *testing.T) {
	for _, scope := range []string{"five_hour", "seven_day"} {
		t.Run(scope, func(t *testing.T) {
			p, s, tr, req := backupRuntime(t)
			tr.serve = func(r *http.Request, n int) (int, http.Header, string) {
				if n == 0 {
					return backupReject(scope)
				}
				return backupOK("end_turn")
			}
			msg := finishBackup(t, p, context.Background(), req)
			if len(tr.headers) != 2 || tr.headers[0].Get("Authorization") == "" || tr.headers[1].Get("X-API-Key") != "fake-api-backup" {
				t.Fatalf("OAuth→API dispatches=%v", tr.headers)
			}
			if string(tr.bodies[0]) != string(tr.bodies[1]) {
				t.Fatal("fallback changed wire prefix/body")
			}
			if tr.headers[1].Get("Authorization") != "" || strings.Contains(tr.headers[1].Get("anthropic-beta"), "oauth-") {
				t.Fatal("API used OAuth headers")
			}
			if msg.ProviderSource == nil || msg.ProviderSource.Kind != "api_backup" {
				t.Fatal("API source missing in history")
			}
			if snap, err := s.PeekSnapshot("anthropic"); err != nil || snap.Kind != "oauth" {
				t.Fatal("lost OAuth")
			}
			finishBackup(t, p, context.Background(), req)
			if len(tr.headers) != 3 || tr.headers[2].Get("Authorization") == "" {
				t.Fatal("next ordinary request did not prefer OAuth")
			}
		})
	}
}
func TestBackupRuntimeGeneric429DoesNotPay(t *testing.T) {
	p, _, tr, req := backupRuntime(t)
	tr.serve = func(*http.Request, int) (int, http.Header, string) {
		return 429, http.Header{}, `{"error":{"type":"rate_limit_error"}}`
	}
	_, err := p.Stream(context.Background(), req)
	var ready *core.ProviderRetryReady
	if !errors.As(err, &ready) || len(tr.headers) != 1 || tr.headers[0].Get("X-API-Key") != "" {
		t.Fatalf("generic retry %v requests=%v", err, tr.headers)
	}
}
func TestBackupRuntimeAPICreditsErrorPreservesOAuth(t *testing.T) {
	p, s, tr, req := backupRuntime(t)
	tr.serve = func(r *http.Request, n int) (int, http.Header, string) {
		if n == 0 {
			return backupReject("seven_day")
		}
		return 400, http.Header{}, `{"error":{"type":"invalid_request_error","message":"Your credit balance is too low"}}`
	}
	_, err := p.Stream(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "credit") || len(tr.headers) != 2 {
		t.Fatalf("want visible API error: %v requests=%d", err, len(tr.headers))
	}
	if snap, e := s.PeekSnapshot("anthropic"); e != nil || snap.Kind != "oauth" {
		t.Fatal("API denial invalidated OAuth")
	}
}
func TestBackupRuntimeStopConfigAndRevokeBeforeDispatch(t *testing.T) {
	for _, what := range []string{"stop", "config", "replace", "remove"} {
		t.Run(what, func(t *testing.T) {
			p, s, tr, req := backupRuntime(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			prepared := false
			tr.serve = func(*http.Request, int) (int, http.Header, string) { return backupReject("seven_day") }
			req.Options.OnProviderDispatch = func(_ context.Context, source core.ProviderSource) error {
				if source.Kind != "api_backup" {
					return nil
				}
				prepared = true
				if what == "stop" {
					cancel()
					return ctx.Err()
				}
				if what == "config" {
					return core.ErrProviderReconfigured
				}
				return nil
			}
			// Store mutations win before backup selection, not inside its lock.
			if what == "replace" || what == "remove" {
				tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
					if n != 0 {
						t.Fatal("revoked dispatch")
					}
					st, e := s.AnthropicBackupStatus()
					if e != nil {
						t.Fatal(e)
					}
					switch what {
					case "replace":
						_, e = s.SaveAnthropicAPIKey(st.Revision.Primary, "replaced-fake-key")
					case "remove":
						e = s.RemoveAnthropicAPIKey(st.Revision.Key)
					}
					if e != nil {
						t.Fatal(e)
					}
					return backupReject("seven_day")
				}
			}
			_, err := p.Stream(ctx, req)
			if err == nil || len(tr.headers) != 1 {
				t.Fatalf("%s allowed dispatch: %v %d", what, err, len(tr.headers))
			}
			if (what == "stop" || what == "config") && !prepared {
				t.Fatal("test never reached backup admission")
			}
		})
	}
}
func TestBackupRuntimeContinuationOriginAndRevoke(t *testing.T) {
	p, s, tr, req := backupRuntime(t)
	tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
		if n == 0 {
			return backupReject("five_hour")
		}
		return backupOK("pause_turn")
	}
	first := finishBackup(t, p, context.Background(), req)
	if first.ProviderSource == nil {
		t.Fatal("binding missing")
	}
	req.Options.ProviderBinding = first.ProviderSource
	finishBackup(t, p, context.Background(), req)
	if len(tr.headers) != 3 || tr.headers[2].Get("X-API-Key") == "" {
		t.Fatal("bound continuation changed origin")
	}
	st, err := s.AnthropicBackupStatus()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAnthropicAPIKey(st.Revision.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stream(context.Background(), req); err == nil || len(tr.headers) != 3 {
		t.Fatal("revoked bound chain changed account or dispatched")
	}
}
