package provider

import (
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestNew_Anthropic(t *testing.T) {
	model := core.Model{Provider: "anthropic", ID: "claude-sonnet-4-20250514"}
	p, err := New(model, Config{APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNew_OpenAI_APIKey(t *testing.T) {
	model := core.Model{Provider: "openai", ID: "gpt-4.1-2025-04-14"}
	p, err := New(model, Config{APIKey: "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNew_OpenAI_OAuth(t *testing.T) {
	model := core.Model{Provider: "openai", ID: "gpt-4.1-2025-04-14"}
	p, err := New(model, Config{APIKey: "oauth-token", IsOAuth: true, AccountID: "acc-123"})
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestNew_EmptyProvider_Errors(t *testing.T) {
	model := core.Model{Provider: "", ID: "some-model"}
	_, err := New(model, Config{APIKey: "key"})
	if err == nil {
		t.Fatal("expected error for empty provider")
	}
}

func TestNew_UnknownProvider_Errors(t *testing.T) {
	model := core.Model{Provider: "gemini", ID: "gemini-pro"}
	_, err := New(model, Config{APIKey: "key"})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}
