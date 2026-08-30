package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
)

func TestParseAllowPattern_Valid(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Bash(go:*)", "Bash(go:*)"},
		{"  Write(*.go)  ", "Write(*.go)"},
		{"edit", "edit"},
	}
	for _, tt := range tests {
		got, err := parseAllowPattern(tt.input)
		if err != nil {
			t.Errorf("parseAllowPattern(%q) error: %v", tt.input, err)
		}
		if got != tt.want {
			t.Errorf("parseAllowPattern(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAuxiliaryModelResolver_UsesCompletionCredentialsOnly(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	store := newTestAuthStore(t)
	resolve := auxiliaryModelResolver(store)

	if _, enabled, err := resolve("auto"); err != nil || enabled {
		t.Fatalf("no completion credentials: enabled=%v err=%v", enabled, err)
	}
	if _, enabled, err := resolve("luna"); err != nil || enabled {
		t.Fatalf("explicit Luna without completion credentials: enabled=%v err=%v", enabled, err)
	}
	if err := store.Set("openai-transcribe", auth.Credential{Type: "api_key", Key: "transcribe-only"}); err != nil {
		t.Fatal(err)
	}
	if _, enabled, err := resolve("auto"); err != nil || enabled {
		t.Fatalf("transcribe-only credentials: enabled=%v err=%v", enabled, err)
	}
	if _, enabled, err := resolve("luna"); err != nil || enabled {
		t.Fatalf("explicit Luna with transcribe-only credentials: enabled=%v err=%v", enabled, err)
	}
	if err := store.Set("anthropic", auth.Credential{Type: "api_key", Key: "anthropic-key"}); err != nil {
		t.Fatal(err)
	}
	if model, enabled, err := resolve("auto"); err != nil || !enabled || model.ID != "claude-haiku-4-5-20251001" {
		t.Fatalf("Anthropic fallback = %+v, %v, %v", model, enabled, err)
	}
	if err := store.Set("openai", auth.Credential{Type: "api_key", Key: "openai-key"}); err != nil {
		t.Fatal(err)
	}
	if model, enabled, err := resolve("auto"); err != nil || !enabled || model.ID != "gpt-5.6-luna" {
		t.Fatalf("OpenAI priority = %+v, %v, %v", model, enabled, err)
	}
}

func TestBuildProvider_XAIStoredOAuth(t *testing.T) {
	store := newTestAuthStore(t)
	if err := store.Set("xai", auth.Credential{Type: "oauth", Access: "consumer-token", Refresh: "refresh", Expires: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	built, err := buildProvider(core.Model{Provider: "xai", ID: "grok-4.5"}, store)
	if err != nil || built.Provider == nil || built.AuthNotice == "" {
		t.Fatalf("build xAI OAuth = %#v, %v", built, err)
	}
}

func TestNewAnthropicUsagePoller_StoredXAIAPIKeyIsPlanUnsupported(t *testing.T) {
	t.Setenv("XAI_API_KEY", "")
	store := newTestAuthStore(t)
	if err := store.Set("xai", auth.Credential{Type: "api_key", Key: "xai-key"}); err != nil {
		t.Fatal(err)
	}
	poller := newAnthropicUsagePoller(store)
	status, ok := poller.StaticProviderStatus("xai")
	if !ok || status.AuthKind != "api_key" || status.Reason != "plan_unsupported" {
		t.Fatalf("xAI status = %#v, found=%v", status, ok)
	}
}

func TestParseAllowPattern_Empty(t *testing.T) {
	for _, input := range []string{"", "  ", "\t"} {
		_, err := parseAllowPattern(input)
		if err == nil {
			t.Errorf("parseAllowPattern(%q) should return error", input)
		}
	}
}

func TestParseAllowPattern_Repeated(t *testing.T) {
	// Simulate repeated --allow flags
	var patterns []string
	inputs := []string{"Bash(go:*)", "Write(*.go)", "Bash(npm:*)"}
	for _, val := range inputs {
		parsed, err := parseAllowPattern(val)
		if err != nil {
			t.Fatal(err)
		}
		patterns = append(patterns, parsed)
	}
	if len(patterns) != 3 {
		t.Errorf("expected 3 patterns, got %d", len(patterns))
	}
}

type captureProvider struct {
	keys []string
}

func (p *captureProvider) Stream(_ context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	p.keys = append(p.keys, req.Options.APIKey)
	ch := make(chan core.AssistantEvent)
	close(ch)
	return ch, nil
}

func TestRefreshingProvider_UsesLatestKeyEachRequest(t *testing.T) {
	store := newTestAuthStore(t)
	base := &captureProvider{}
	prov := &refreshingProvider{
		base:         base,
		providerName: "anthropic",
		authStore:    store,
	}

	t.Setenv("ANTHROPIC_API_KEY", "key-1")
	if _, err := prov.Stream(context.Background(), core.Request{}); err != nil {
		t.Fatalf("first Stream error: %v", err)
	}

	t.Setenv("ANTHROPIC_API_KEY", "key-2")
	if _, err := prov.Stream(context.Background(), core.Request{}); err != nil {
		t.Fatalf("second Stream error: %v", err)
	}

	if got, want := base.keys, []string{"key-1", "key-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("api keys passed = %#v, want %#v", got, want)
	}
}

// TestBuildProvider_ExpiredOAuthStillBuilds reproduces the lockout: with the
// default model on Anthropic and a refresh token the server rejects, building
// the provider failed, so creating a session — and reopening an existing one —
// returned 500. From a phone that left no way back in.
//
// The key fetched at build time is never used anyway: refreshingProvider gets a
// fresh one per Stream. So the session must open, and sending must be what
// reports the dead credential.
func TestBuildProvider_ExpiredOAuthStillBuilds(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	store := newTestAuthStore(t)
	// Expired access plus a refresh token the server will reject: GetAPIKey
	// attempts a refresh and fails, exactly as it did in production.
	if err := store.Set("anthropic", auth.Credential{
		Type:    "oauth",
		Access:  "dead-access",
		Refresh: "rotated-away",
		Expires: time.Now().Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := store.GetAPIKey("anthropic"); err == nil {
		t.Fatal("precondition: GetAPIKey should fail with an expired refresh token")
	}

	built, err := buildProvider(core.Model{Provider: "anthropic", ID: "claude-opus-4"}, store)
	if err != nil {
		t.Fatalf("buildProvider returned %v: a session must still open with dead credentials", err)
	}
	if built.Provider == nil {
		t.Fatal("buildProvider returned no provider: the session would have nothing to send with")
	}
}
