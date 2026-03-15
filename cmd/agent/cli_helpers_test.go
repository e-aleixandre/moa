package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
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
