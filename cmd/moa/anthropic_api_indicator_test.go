package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

func TestAPIIndicatorClearsOnAnotherTransport(t *testing.T) {
	for _, target := range []string{"openai", "anthropic_env"} {
		t.Run(target, func(t *testing.T) {
			p, store, tr, _ := backupRuntime(t)
			reg := core.NewRegistry()
			a, _, _ := newBackupAgent(t, p, reg)
			var observed, working core.ProviderExecution
			var mu sync.Mutex
			unsubscribe := a.Subscribe(func(ev core.AgentEvent) {
				if ev.ProviderExecution != nil && ev.ProviderExecution.Phase == "working" && ev.ProviderExecution.Model != p.model.ID {
					mu.Lock()
					working = *ev.ProviderExecution
					mu.Unlock()
				}
			})
			defer unsubscribe()
			sends := 0
			f := newFakeUpstream(t)
			f.inference = func(w http.ResponseWriter, c upstreamCall, _ int) {
				if target == "openai" && (c.Origin != "chatgpt.com" || c.Bearer == "" || c.APIKey != "") {
					t.Error("fixture did not dispatch OpenAI OAuth")
				}
				mu.Lock()
				sends++
				observed = a.ProviderExecution()
				mu.Unlock()
				writeOK(w, c)
			}
			if err := reg.Register(core.Tool{Name: "backup_once", Parameters: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, map[string]any, func(core.Result)) (core.Result, error) {
				before := a.ProviderExecution()
				if before.Source == nil || before.Source.Kind != "api_backup" {
					t.Error("fixture did not enter API fallback")
				}
				if target == "openai" {
					if _, err := store.CommitLogin("openai", "", auth.Credential{Type: "oauth", Access: openaiAccess("review-account", "review-fake-signature"), Refresh: "fake-openai-refresh", AccountID: "review-account", Expires: 4102444800000}); err != nil {
						return core.TextResult("failed"), err
					}
					model := core.Model{ID: "gpt-6.1-sol", Provider: "openai"}
					next := &snapshotProvider{model: model, authStore: store, httpClient: f.client()}
					return core.TextResult("switch provider"), a.SetModel(next, model)
				}
				t.Setenv("ANTHROPIC_API_KEY", "fake-env-primary")
				// Same provider, different primary path; force the following
				// natural boundary past the previous key binding.
				model := core.Model{ID: "claude-sonnet-5-5", Provider: "anthropic"}
				next := &snapshotProvider{model: model, authStore: store, httpClient: f.client()}
				return core.TextResult("environment primary"), a.SetModel(next, model)
			}}); err != nil {
				t.Fatal(err)
			}
			tr.serve = func(_ *http.Request, n int) (int, http.Header, string) {
				if n == 0 {
					return backupReject("five_hour")
				}
				return 200, http.Header{}, backupToolResponse(t, tr.bodies[n], "change-model")
			}
			if _, err := a.Send(context.Background(), "switch on a tool boundary"); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(tr.headers) != 2 || sends != 1 {
				t.Fatalf("fixture: Anthropic sends=%d next sends=%d", len(tr.headers), sends)
			}
			t.Logf("target=%s next_provider=%s next_model=%s phase=%s source=%+v; working_source=%+v", target, observed.Provider, observed.Model, observed.Phase, observed.Source, working.Source)
			if observed.Source != nil && observed.Source.Kind == "api_backup" {
				t.Error("Anthropic fallback API indicator survives a real non-plan dispatch")
			}
			if working.Phase != "working" || working.Model != observed.Model {
				t.Fatal("fixture did not reach working on the new provider")
			}
			if working.Source != nil && working.Source.Kind == "api_backup" {
				t.Error("API indicator remains set during the new provider's stream")
			}
			if a.ProviderExecution().Phase != "" {
				t.Fatal("run terminal phase not reset")
			}
		})
	}
}
