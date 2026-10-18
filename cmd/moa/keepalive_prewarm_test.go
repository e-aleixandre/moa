package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func prewarmOK(*http.Request, int) (int, http.Header, string) {
	return 200, http.Header{}, `{"content":[],"stop_reason":"max_tokens","usage":{"input_tokens":3,"output_tokens":0,"cache_read_input_tokens":250000,"cache_creation_input_tokens":0}}`
}

// The ping replays the last request on the plan sign-in that served it,
// reusing the source the plan stream reported.
func TestSnapshotPrewarmUsesThePlanThatServedTheLastRequest(t *testing.T) {
	p, _, tr, req := backupRuntime(t)
	tr.serve = func(r *http.Request, i int) (int, http.Header, string) { return backupOK("end_turn") }
	msg := finishBackup(t, p, context.Background(), req)
	if msg.ProviderSource == nil || msg.ProviderSource.Kind != "oauth" {
		t.Fatalf("plan stream source = %+v", msg.ProviderSource)
	}
	tr.serve = prewarmOK
	ping := req
	ping.Options = core.StreamOptions{ProviderBinding: msg.ProviderSource}
	pw, ok := core.ProviderPrewarmer(p)
	if !ok {
		t.Fatal("snapshotProvider cannot prewarm")
	}
	usage, err := pw.Prewarm(context.Background(), ping)
	if err != nil {
		t.Fatal(err)
	}
	if usage.CacheRead != 250000 || usage.Output != 0 {
		t.Fatalf("usage = %+v", usage)
	}
	if len(tr.bodies) != 2 {
		t.Fatalf("requests = %d", len(tr.bodies))
	}
	var real, warm map[string]any
	_ = json.Unmarshal(tr.bodies[0], &real)
	_ = json.Unmarshal(tr.bodies[1], &warm)
	if warm["stream"] != false || warm["max_tokens"] != float64(0) {
		t.Fatalf("ping stream=%v max_tokens=%v", warm["stream"], warm["max_tokens"])
	}
	delete(real, "stream")
	delete(real, "max_tokens")
	delete(warm, "stream")
	delete(warm, "max_tokens")
	rb, _ := json.Marshal(real)
	wb, _ := json.Marshal(warm)
	if string(rb) != string(wb) {
		t.Fatalf("ping body differs from the real one:\nreal: %s\nping: %s", rb, wb)
	}
	if got, want := tr.headers[1].Get("Authorization"), tr.headers[0].Get("Authorization"); got == "" || got != want {
		t.Fatalf("ping auth %q, real %q", got, want)
	}
	if got, want := tr.headers[1].Get("anthropic-beta"), tr.headers[0].Get("anthropic-beta"); got != want {
		t.Fatalf("ping betas %q, real %q", got, want)
	}
}

func TestSnapshotPrewarmNeverPingsAnotherSourceOrTheBackup(t *testing.T) {
	p, _, tr, req := backupRuntime(t)
	tr.serve = func(*http.Request, int) (int, http.Header, string) {
		t.Error("a refused ping reached the network")
		return prewarmOK(nil, 0)
	}
	for name, binding := range map[string]*core.ProviderSource{
		"no source":         nil,
		"api backup":        {Kind: "api_backup", WireProfile: "oauth", Provider: "anthropic", Model: "claude-opus-5-5"},
		"another sign-in":   {Kind: "oauth", PrimaryGeneration: "an-older-login", WireProfile: "oauth", Provider: "anthropic", Model: "claude-opus-5-5"},
		"an api key source": {Kind: "api_key", Provider: "anthropic", Model: "claude-opus-5-5"},
	} {
		ping := req
		ping.Options = core.StreamOptions{ProviderBinding: binding}
		if _, err := p.Prewarm(context.Background(), ping); !errors.Is(err, core.ErrPrewarmUnsupported) {
			t.Errorf("%s: err = %v, want ErrPrewarmUnsupported", name, err)
		}
	}
	other := &snapshotProvider{model: core.Model{ID: "gpt-6.1-sol", Provider: "openai"}, authStore: p.authStore, httpClient: p.httpClient}
	if _, err := other.Prewarm(context.Background(), req); !errors.Is(err, core.ErrPrewarmUnsupported) {
		t.Errorf("openai: err = %v", err)
	}
}
