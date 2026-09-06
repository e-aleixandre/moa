package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/e-aleixandre/moa/pkg/auth"
	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider"
)

// ProviderBuildResult wraps a provider with optional auth notice.
type ProviderBuildResult struct {
	Provider   core.Provider
	AuthNotice string
}

// refreshingProvider injects a fresh API key into each Stream request.
// This enables OAuth refresh during long-running sessions without restart.
type refreshingProvider struct {
	base         core.Provider
	providerName string
	authStore    *auth.Store
}

func (p *refreshingProvider) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	apiKey, _, err := p.authStore.GetAPIKey(p.providerName)
	if err != nil {
		return nil, err
	}
	req.Options.APIKey = apiKey
	return p.base.Stream(ctx, req)
}

// Unwrap exposes the provider decorated with API-key refreshing so optional
// provider capabilities remain available to callers.
func (p *refreshingProvider) Unwrap() core.Provider {
	return p.base
}

// buildProvider creates the appropriate provider based on the model's Provider field.
// Side-effect free: it writes nothing, so callers own their own output.
func buildProvider(model core.Model, authStore *auth.Store) (ProviderBuildResult, error) {
	providerName := model.Provider
	if providerName == "" {
		providerName = "anthropic"
	}

	// Deliberately not fatal. The key fetched here is never the one used:
	// refreshingProvider asks for a fresh one on every Stream. Failing the
	// build meant expired credentials blocked creating a session and even
	// reopening an existing one, which left no way back in from a phone. The
	// honest failure belongs at send time, where the token is really needed.
	apiKey, isOAuth, err := authStore.GetAPIKey(providerName)
	if err != nil {
		apiKey, isOAuth = "", authStore.CredentialKind(providerName) == "oauth"
	}

	cfg := provider.Config{
		APIKey:  apiKey,
		IsOAuth: isOAuth,
	}
	if providerName == "xai" || providerName == "meta" {
		cfg.AuthKind = provider.AuthKindAPIKey
		if authStore.CredentialKind(providerName) == "oauth" {
			cfg.AuthKind = provider.AuthKindOAuth
		}
	}

	var authNotice string
	switch providerName {
	case "openai":
		if isOAuth {
			cfg.AccountID = authStore.GetAccountID("openai")
			// chatgpt.com can reject an access token before its advertised
			// expiry, so the proactive expiry check alone is not enough: the
			// transport needs a reactive refresh path for rejected tokens.
			cfg.RefreshOAuth = func(rejected string) (string, error) {
				return authStore.RefreshOAuthIfCurrent("openai", rejected)
			}
			authNotice = "ChatGPT subscription OAuth"
		}
	case "anthropic":
		if isOAuth {
			authNotice = "Claude Max OAuth"
		}
	case "xai":
		if isOAuth {
			cfg.RefreshOAuth = func(rejected string) (string, error) {
				return authStore.RefreshOAuthIfCurrent("xai", rejected)
			}
			authNotice = "SuperGrok/X subscription OAuth"
		}
	case "meta":
		if isOAuth {
			// The rejected value here is the minted Model API key, not the
			// access token; the store re-mints it from the OAuth session.
			cfg.RefreshOAuth = func(rejected string) (string, error) {
				return authStore.RefreshOAuthIfCurrent("meta", rejected)
			}
			authNotice = "Muse subscription OAuth"
		}
	}

	m := model
	m.Provider = providerName
	p, err := provider.New(m, cfg)
	if err != nil {
		return ProviderBuildResult{}, err
	}

	wrapped := &refreshingProvider{
		base:         p,
		providerName: providerName,
		authStore:    authStore,
	}
	return ProviderBuildResult{Provider: wrapped, AuthNotice: authNotice}, nil
}

// auxiliaryModelResolver uses only a provider's normal completion credential.
// In particular, the dedicated openai-transcribe credential does not make Luna
// available for titles or briefs.
func auxiliaryModelResolver(authStore *auth.Store) func(string) (core.Model, bool, error) {
	return func(spec string) (core.Model, bool, error) {
		return core.ResolveAuxiliaryModel(spec, func(provider string) bool {
			key, _, err := authStore.GetAPIKey(provider)
			return err == nil && key != ""
		})
	}
}

// compactSummarizerResolver builds the summarizer for compaction: the model
// configured in `compact_model`, with its own provider, or the session's own
// model when none is configured.
//
// It never fails a compaction. A session that stops compacting grows until it
// hits the window, which is worse than a summary billed to a pricier model, so
// an unresolvable choice falls back to the session's model and returns the
// reason for the transcript to show. A nil provider means "use the session's
// own", which the agent already handles.
func compactSummarizerResolver(
	authStore *auth.Store,
	providerFactory func(core.Model) (core.Provider, error),
	spec func() string,
) func(core.Model) (core.Provider, core.Model, string) {
	return func(sessionModel core.Model) (core.Provider, core.Model, string) {
		configured := ""
		if spec != nil {
			configured = spec()
		}
		model, fallback := core.ResolveCompactModel(configured, sessionModel, func(provider string) bool {
			key, _, err := authStore.GetAPIKey(provider)
			return err == nil && key != ""
		})
		if fallback != core.CompactModelFallbackNone {
			return nil, core.Model{}, core.CompactModelFallbackNotice(fallback, configured, sessionModel)
		}
		if model.ID == sessionModel.ID {
			return nil, core.Model{}, ""
		}
		prov, err := providerFactory(model)
		if err != nil {
			// The credential looked usable but the provider would not build.
			// Same rule: compact anyway, and say why the choice was not kept.
			return nil, core.Model{}, core.CompactModelFallbackNotice(
				core.CompactModelFallbackNoCredential, configured, sessionModel)
		}
		return prov, model, ""
	}
}

func printAuthNotice(w io.Writer, notice string) {
	if notice == "" {
		return
	}
	_, _ = fmt.Fprintf(w, "\033[90m(using %s)\033[0m\n", notice)
}

// parseAllowPattern validates and normalizes a --allow flag value.
func parseAllowPattern(val string) (string, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return "", fmt.Errorf("allow pattern cannot be empty")
	}
	return val, nil
}

// resolvePrompt resolves the prompt from flag, @file, or stdin pipe. A run
// always needs one: there is no interactive frontend to fall back to.
func resolvePrompt(p string) (string, error) {
	if p != "" {
		if strings.HasPrefix(p, "@") {
			filePath := strings.TrimPrefix(p, "@")
			data, err := os.ReadFile(filePath)
			if err != nil {
				return "", fmt.Errorf("reading prompt file %s: %w", filePath, err)
			}
			content := strings.TrimSpace(string(data))
			if content == "" {
				return "", fmt.Errorf("prompt file %s is empty", filePath)
			}
			return content, nil
		}
		return p, nil
	}

	fi, err := os.Stdin.Stat()
	if err != nil {
		return "", nil
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			return "", fmt.Errorf("stdin is empty")
		}
		return content, nil
	}

	return "", fmt.Errorf("no prompt provided: use -p \"text\", -p @file, or pipe to stdin")
}

// providerNameForModel is the provider a model authenticates against, with the
// same default buildProvider applies.
func providerNameForModel(model core.Model) string {
	if model.Provider == "" {
		return "anthropic"
	}
	return model.Provider
}
