package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
)

func upgradePathsReject(t *testing.T) (int, http.Header, string) {
	t.Helper()
	data, err := os.ReadFile("../../pkg/provider/anthropic/testdata/weekly-upgrade-paths.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Status  int             `json:"status"`
		Headers http.Header     `json:"headers"`
		Body    json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	return capture.Status, capture.Headers, string(capture.Body)
}

func TestBackupUpgradePathsOneToolSameTurnAndNaturalRestore(t *testing.T) {
	p, _, tr, _ := backupRuntime(t)
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		reg := core.NewRegistry()
		tools := 0
		if err := reg.Register(core.Tool{Name: "backup_once", Parameters: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
			tools++
			return core.TextResult("executed once"), nil
		}}); err != nil {
			t.Fatal(err)
		}
		a, _, _ := newBackupAgent(t, p, reg)
		tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
			if n == 0 {
				return upgradePathsReject(t)
			}
			if n == 1 {
				return 200, http.Header{}, backupToolResponse(t, tr.bodies[n], "one-tool")
			}
			return backupOK("end_turn")
		}
		if _, err := a.SendWithMsgID(ctx, "offline", "stable-user"); err != nil {
			t.Fatal(err)
		}
		users, results := 0, 0
		for _, m := range a.Messages() {
			if m.Role == "user" {
				users++
				if m.MsgID != "stable-user" {
					t.Fatal("turn identity changed")
				}
			}
			if m.Role == "tool_result" {
				results++
			}
		}
		if len(tr.headers) != 3 || tools != 1 || users != 1 || results != 1 {
			t.Fatalf("HTTP calls=%d tools=%d users=%d results=%d", len(tr.headers), tools, users, results)
		}
		for n, api := range []bool{false, true, true} {
			if (tr.headers[n].Get("X-API-Key") != "") != api {
				t.Fatalf("wrong source at %d", n)
			}
			var body struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(tr.bodies[n], &body) != nil || body.Model != "claude-opus-5-5" {
				t.Fatal("model changed")
			}
		}
		if string(tr.bodies[0]) != string(tr.bodies[1]) {
			t.Fatal("fallback changed the rejected request body")
		}
		// The capture reports its weekly reset (~45h later): until then the
		// next ordinary request goes straight to the API, then back to the plan.
		if _, err := a.Send(ctx, "offline"); err != nil {
			t.Fatal(err)
		}
		if len(tr.headers) != 4 || tr.headers[3].Get("X-API-Key") == "" || tr.headers[3].Get("Authorization") != "" || tools != 1 {
			t.Fatal("next request before the reset probed the plan or repeated a tool")
		}
		time.Sleep(46 * time.Hour)
		if _, err := a.Send(context.Background(), "offline"); err != nil {
			t.Fatal(err)
		}
		if len(tr.headers) != 5 || tr.headers[4].Get("Authorization") == "" || tools != 1 {
			t.Fatal("next ordinary request after the reset did not prefer OAuth or repeated a tool")
		}
	})
}

func TestBackupUpgradePathsStopAndRemoveBeforeDispatch(t *testing.T) {
	for _, action := range []string{"stop", "remove"} {
		t.Run(action, func(t *testing.T) {
			p, store, tr, req := backupRuntime(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
				if n != 0 {
					t.Fatal("paid send escaped Stop or revocation")
				}
				return upgradePathsReject(t)
			}
			prepared := false
			req.Options.OnProviderPrepare = func(context.Context, core.ProviderSource) error {
				prepared = true
				if action == "stop" {
					cancel()
					return ctx.Err()
				}
				st, err := store.AnthropicBackupStatus()
				if err != nil {
					t.Fatal(err)
				}
				if err := store.RemoveAnthropicAPIKey(st.Revision.Key); err != nil {
					t.Fatal(err)
				}
				return nil
			}
			_, err := p.Stream(ctx, req)
			if !prepared || err == nil || len(tr.headers) != 1 {
				t.Fatalf("action=%s prepared=%v err=%v HTTP calls=%d", action, prepared, err, len(tr.headers))
			}
			if action == "stop" && !errors.Is(err, context.Canceled) {
				t.Fatalf("Stop returned %v instead of cancellation", err)
			}
		})
	}
}

type upgradePathsNetworkError struct {
	calls int
	paid  bool
}

func (tr *upgradePathsNetworkError) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.calls++
	tr.paid = tr.paid || r.Header.Get("X-API-Key") != ""
	return nil, io.ErrUnexpectedEOF
}

func TestBackupUpgradePathsNetworkErrorDoesNotPay(t *testing.T) {
	p, _, _, req := backupRuntime(t)
	tr := &upgradePathsNetworkError{}
	p.httpClient.Transport = tr
	_, err := p.Stream(context.Background(), req)
	var ready *core.ProviderRetryReady
	if !errors.As(err, &ready) || ready.Wait == nil || ready.Wait.Kind != "transport_retry" || ready.Wait.Status != 0 || tr.calls != 1 || tr.paid {
		t.Fatalf("network error paid or lost retry: %v HTTP calls=%d paid=%v", err, tr.calls, tr.paid)
	}
}
