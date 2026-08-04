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
