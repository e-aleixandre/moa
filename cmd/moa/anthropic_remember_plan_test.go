package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

// backupRejectUntil is a confirmed plan limit whose reset Anthropic reports
// completely (window reset and global reset agree).
func backupRejectUntil(scope string, reset time.Time) (int, http.Header, string) {
	status, h, body := backupReject(scope)
	window := "5h"
	if scope == "seven_day" {
		window = "7d"
	}
	epoch := strconv.FormatInt(reset.Unix(), 10)
	h.Set("anthropic-ratelimit-unified-"+window+"-reset", epoch)
	h.Set("anthropic-ratelimit-unified-reset", epoch)
	return status, h, body
}

func planCalls(headers []http.Header) (plan, api int) {
	for _, h := range headers {
		if h.Get("Authorization") != "" {
			plan++
		}
		if h.Get("X-API-Key") != "" {
			api++
		}
	}
	return plan, api
}

func TestRememberedPlanLimitSkipsPlanAcrossSendsAndSessions(t *testing.T) {
	for _, scope := range []string{"five_hour", "seven_day"} {
		t.Run(scope, func(t *testing.T) {
			p, _, tr, _ := backupRuntime(t)
			reset := time.Now().Add(2 * time.Hour)
			tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
				if n == 0 {
					return backupRejectUntil(scope, reset)
				}
				return backupOK("end_turn")
			}
			a, _, _ := newBackupAgent(t, p, core.NewRegistry())
			if _, err := a.Send(context.Background(), "first"); err != nil {
				t.Fatal(err)
			}
			if plan, api := planCalls(tr.headers); plan != 1 || api != 1 {
				t.Fatalf("confirming send: plan=%d api=%d", plan, api)
			}
			other := &snapshotProvider{model: p.model, authStore: p.authStore, httpClient: p.httpClient}
			b, _, _ := newBackupAgent(t, other, core.NewRegistry())
			for i, send := range []func() error{
				func() error { _, err := a.Send(context.Background(), "second"); return err },
				func() error { _, err := b.Send(context.Background(), "other session"); return err },
				func() error { _, err := a.Send(context.Background(), "third"); return err },
			} {
				if err := send(); err != nil {
					t.Fatalf("send %d: %v", i, err)
				}
			}
			plan, api := planCalls(tr.headers[2:])
			if plan != 0 || api != 3 {
				t.Fatalf("before reset: plan=%d api=%d, want 0 and 3", plan, api)
			}
			for _, body := range tr.bodies[2:] {
				if !strings.Contains(string(body), `"model":"claude-opus-5-5"`) {
					t.Fatalf("API send changed model: %s", body)
				}
			}
		})
	}
}

func TestRememberedPlanLimitExpiresAtReset(t *testing.T) {
	p, _, tr, req := backupRuntime(t)
	reset := time.Now().Add(time.Hour)
	tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
		if n == 0 {
			return backupRejectUntil("seven_day", reset)
		}
		return backupOK("end_turn")
	}
	finishBackup(t, p, context.Background(), req)
	p.now = func() time.Time { return reset.Add(time.Minute) }
	finishBackup(t, p, context.Background(), req)
	finishBackup(t, p, context.Background(), req)
	if len(tr.headers) != 4 || tr.headers[2].Get("Authorization") == "" || tr.headers[3].Get("Authorization") == "" {
		t.Fatalf("after reset the plan was not tried and kept: %v", tr.headers)
	}
}

func TestRememberedPlanLimitRenewsOnNewConfirmation(t *testing.T) {
	p, _, tr, req := backupRuntime(t)
	first := time.Now().Add(time.Hour)
	second := time.Now().Add(5 * time.Hour)
	tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
		switch n {
		case 0:
			return backupRejectUntil("five_hour", first)
		case 2:
			return backupRejectUntil("seven_day", second)
		}
		return backupOK("end_turn")
	}
	finishBackup(t, p, context.Background(), req)
	p.now = func() time.Time { return first.Add(time.Minute) }
	finishBackup(t, p, context.Background(), req)
	finishBackup(t, p, context.Background(), req)
	if plan, api := planCalls(tr.headers); len(tr.headers) != 5 || plan != 2 || api != 3 {
		t.Fatalf("renewed limit: plan=%d api=%d calls=%d", plan, api, len(tr.headers))
	}
}

func TestRememberedPlanLimitIgnoresChangedCredentials(t *testing.T) {
	for _, change := range []string{"oauth", "key"} {
		t.Run(change, func(t *testing.T) {
			p, s, tr, req := backupRuntime(t)
			tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
				if n == 0 {
					return backupRejectUntil("seven_day", time.Now().Add(time.Hour))
				}
				return backupOK("end_turn")
			}
			finishBackup(t, p, context.Background(), req)
			status, err := s.AnthropicBackupStatus()
			if err != nil {
				t.Fatal(err)
			}
			if change == "oauth" {
				if _, err := s.CommitLogin("anthropic", status.Revision.Primary, auth.Credential{Type: "oauth", Access: "fake-new-access", Refresh: "fake-new-refresh", Expires: 4102444800000}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := s.SaveAnthropicAPIKey(status.Revision.Primary, "fake-other-key"); err != nil {
				t.Fatal(err)
			}
			finishBackup(t, p, context.Background(), req)
			if len(tr.headers) != 3 || tr.headers[2].Get("Authorization") == "" {
				t.Fatalf("remembered limit survived a %s change: %v", change, tr.headers)
			}
		})
	}
}

func TestPlanLimitWithoutReliableResetIsNotRemembered(t *testing.T) {
	cases := map[string]func() (int, http.Header, string){
		"no reset": func() (int, http.Header, string) { return backupReject("seven_day") },
		"beyond cap": func() (int, http.Header, string) {
			return backupRejectUntil("seven_day", time.Now().Add(9*24*time.Hour))
		},
		"generic 429": func() (int, http.Header, string) { return 429, http.Header{}, `{"error":{"type":"rate_limit_error"}}` },
		"retry-after only": func() (int, http.Header, string) {
			s, h, b := backupReject("five_hour")
			h.Set("Retry-After", "3600")
			return s, h, b
		},
	}
	for name, reject := range cases {
		t.Run(name, func(t *testing.T) {
			p, _, tr, req := backupRuntime(t)
			tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
				if n == 0 {
					return reject()
				}
				return backupOK("end_turn")
			}
			if ch, err := p.Stream(context.Background(), req); err == nil {
				for range ch {
				}
			}
			before := len(tr.headers)
			finishBackup(t, p, context.Background(), req)
			if len(tr.headers) != before+1 || tr.headers[before].Get("Authorization") == "" {
				t.Fatalf("next send skipped the plan: %v", tr.headers)
			}
		})
	}
}

func TestRememberedPlanLimitDoesNotForceUnsupportedRequests(t *testing.T) {
	for name, edit := range map[string]func(*core.Request){
		"fast":        func(r *core.Request) { r.Options.Fast = true },
		"other model": func(r *core.Request) { r.Model.ID = "claude-haiku-4-5" },
	} {
		t.Run(name, func(t *testing.T) {
			p, _, tr, req := backupRuntime(t)
			tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
				if n == 0 {
					return backupRejectUntil("seven_day", time.Now().Add(time.Hour))
				}
				return backupOK("end_turn")
			}
			finishBackup(t, p, context.Background(), req)
			unsupported := req
			edit(&unsupported)
			finishBackup(t, p, context.Background(), unsupported)
			if len(tr.headers) != 3 || tr.headers[2].Get("Authorization") == "" || tr.headers[2].Get("X-API-Key") != "" {
				t.Fatalf("unsupported request was forced to the API: %v", tr.headers)
			}
		})
	}
}
