package main

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

// The per-request provider wrapper must not hide deferral: the agent asks it,
// not the concrete provider. Both Anthropic and the OpenAI plan defer.
func TestRuntimeFactoryExposesDeferredTools(t *testing.T) {
	clearProviderEnv(t)
	client := &http.Client{Transport: &round2Capture{}}
	store := auth.NewStoreWithHTTPClient(filepath.Join(t.TempDir(), "auth.json"), client)
	exp := time.Now().Add(time.Hour).UnixMilli()
	if _, err := store.CommitLogin("anthropic", "", auth.Credential{Type: "oauth", Access: "sk-ant-oat01-offline", Refresh: "r", Expires: exp}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitLogin("openai", "", auth.Credential{Type: "oauth", Access: "offline", AccountID: "acct", Expires: exp}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		model core.Model
		want  bool
	}{
		{core.Model{Provider: "anthropic", ID: "claude-opus-5-5"}, true},
		{core.Model{Provider: "openai", ID: "gpt-6.1-sol"}, true},
	} {
		built, err := buildProviderWithClient(c.model, store, client)
		if err != nil {
			t.Fatal(err)
		}
		if got := core.ProviderSupportsDeferredTools(built.Provider, c.model.ID); got != c.want {
			t.Fatalf("%s: deferral %v through %T, want %v", c.model.ID, got, built.Provider, c.want)
		}
	}
}
