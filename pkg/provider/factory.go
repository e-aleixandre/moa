package provider

import (
	"fmt"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/provider/anthropic"
	"github.com/ealeixandre/moa/pkg/provider/openai"
)

// Config holds credentials needed to create a provider.
type Config struct {
	APIKey    string // API key or OAuth access token
	IsOAuth   bool  // Whether APIKey is an OAuth token
	AccountID string // OpenAI OAuth account ID (required when IsOAuth=true for OpenAI)
}

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
	default:
		return nil, fmt.Errorf("unsupported provider: %q (model: %s)", model.Provider, model.ID)
	}
}
