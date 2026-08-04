package provider

import (
	"fmt"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/anthropic"
	"github.com/e-aleixandre/moa/pkg/provider/openai"
	"github.com/e-aleixandre/moa/pkg/provider/xai"
)

// Config holds credentials needed to create a provider.
type Config struct {
	APIKey    string // API key or OAuth access token
	IsOAuth   bool   // Whether APIKey is an OAuth token
	AccountID string // OpenAI OAuth account ID (required when IsOAuth=true for OpenAI)
	AuthKind  AuthKind
	// RefreshOAuth is called only by consumer transports after a rejected OAuth
	// access token. API-key transports never invoke it.
	RefreshOAuth func(rejectedToken string) (string, error)
}

type AuthKind string

const (
	AuthKindAPIKey AuthKind = "api_key"
	AuthKindOAuth  AuthKind = "oauth"
)

// New creates a Provider for the given model.
//
// The model's Provider field determines which implementation to use:
//   - "anthropic": Anthropic API (Claude models)
//   - "openai": OpenAI API (GPT/o-series models). Uses OAuth constructor when IsOAuth=true.
//
// Returns error for unsupported or empty provider names.
func New(model core.Model, cfg Config) (core.Provider, error) {
	switch model.Provider {
	case "anthropic":
		return anthropic.New(cfg.APIKey), nil
	case "openai":
		if cfg.IsOAuth {
			return openai.NewOAuth(cfg.APIKey, cfg.AccountID), nil
		}
		return openai.New(cfg.APIKey), nil
	case "xai":
		switch cfg.AuthKind {
		case AuthKindOAuth:
			return xai.NewOAuth(cfg.APIKey, cfg.RefreshOAuth), nil
		case AuthKindAPIKey:
			return xai.New(cfg.APIKey), nil
		default:
			return nil, fmt.Errorf("xai requires an explicit credential kind")
		}
	default:
		return nil, fmt.Errorf("unsupported provider: %q (model: %s)", model.Provider, model.ID)
	}
}
