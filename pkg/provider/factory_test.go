package provider

import (
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
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

func TestNew_XAI_APIKey(t *testing.T) {
	p, err := New(core.Model{Provider: "xai", ID: "grok-4.5"}, Config{APIKey: "xai-key", AuthKind: AuthKindAPIKey})
	if err != nil || p == nil {
		t.Fatalf("New xAI = %v, %v", p, err)
	}
	if p, err := New(core.Model{Provider: "xai", ID: "grok-4.5"}, Config{APIKey: "consumer-token", AuthKind: AuthKindOAuth}); err != nil || p == nil {
		t.Fatalf("New xAI OAuth = %v, %v", p, err)
	}
}

func TestNew_Meta_RequiresExplicitCredentialKind(t *testing.T) {
	p, err := New(core.Model{Provider: "meta", ID: "muse-spark-1.3"}, Config{APIKey: "meta-key", AuthKind: AuthKindAPIKey})
	if err != nil || p == nil {
		t.Fatalf("New meta = %v, %v", p, err)
	}
	if p, err := New(core.Model{Provider: "meta", ID: "muse-spark-1.3"}, Config{APIKey: "minted-key", AuthKind: AuthKindOAuth}); err != nil || p == nil {
		t.Fatalf("New meta OAuth = %v, %v", p, err)
	}
	if _, err := New(core.Model{Provider: "meta", ID: "muse-spark-1.3"}, Config{APIKey: "meta-key"}); err == nil {
		t.Fatal("meta without a credential kind must fail")
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
