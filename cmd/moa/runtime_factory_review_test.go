package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/agent"
	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/openai"
)

type round2Capture struct{ bodies [][]byte }

func (tr *round2Capture) RoundTrip(r *http.Request) (*http.Response, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	tr.bodies = append(tr.bodies, b)
	w := httptest.NewRecorder()
	writeOK(w, upstreamCall{Origin: r.URL.Host})
	res := w.Result()
	res.Request = r
	return res, nil
}
func round2BuiltProvider(t *testing.T) (core.Provider, *round2Capture, core.Model) {
	t.Helper()
	clearProviderEnv(t)
	tr := &round2Capture{}
	client := &http.Client{Transport: tr}
	store := auth.NewStoreWithHTTPClient(filepath.Join(t.TempDir(), "auth.json"), client)
	_, err := store.CommitLogin("openai", "", auth.Credential{Type: "oauth", Access: "offline-review-no-valid-token", AccountID: "offline-review-account", Expires: time.Now().Add(time.Hour).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	m := core.Model{Provider: "openai", ID: "gpt-6.1-sol"}
	built, err := buildProviderWithClient(m, store, client)
	if err != nil {
		t.Fatal(err)
	}
	return built.Provider, tr, m
}
func TestRound2RuntimeFactoryExposesImplicitCaching(t *testing.T) {
	p, _, m := round2BuiltProvider(t)
	if !core.ProviderHasImplicitPrefixCache(openai.NewOAuth("offline-unused", "offline-account", nil), m.ID) {
		t.Fatal("concrete control not implicit")
	}
	if !core.ProviderHasImplicitPrefixCache(p, m.ID) {
		t.Fatalf("production factory returned %T, which hides implicit-prefix capability", p)
	}
}
func TestRound2RuntimeFactoryKeepsPrefixAcrossRuns(t *testing.T) {
	p, tr, m := round2BuiltProvider(t)
	a, err := agent.New(agent.AgentConfig{Provider: p, Model: m, SystemPrompt: "fixed", MaxTurns: 3, MaxRunDuration: 5 * time.Second, EphemeralContext: func() *core.Message { m := core.NewUserMessage("stable inventory"); return &m }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Send(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if len(tr.bodies) != 2 {
		t.Fatalf("requests=%d", len(tr.bodies))
	}
	var before, next struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(tr.bodies[0], &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(tr.bodies[1], &next); err != nil {
		t.Fatal(err)
	}
	for i, b := range before.Input {
		if i >= len(next.Input) || !bytes.Equal(b, next.Input[i]) {
			t.Fatalf("production wrapper still moves inventory: first mismatch input[%d], %d -> %d items; all HTTP intercepted locally", i, len(before.Input), len(next.Input))
		}
	}
}
