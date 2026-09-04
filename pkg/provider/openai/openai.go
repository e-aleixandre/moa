// Package openai implements core.Provider for the OpenAI Responses API.
// Supports GPT and Codex models with streaming, tool use, and reasoning effort.
// Works with both API keys (api.openai.com) and OAuth (chatgpt.com/backend-api).
package openai

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/responses"
	"github.com/e-aleixandre/moa/pkg/provider/retry"
	"github.com/e-aleixandre/moa/pkg/provider/sseutil"
)

const (
	apiBaseURL   = "https://api.openai.com"
	codexBaseURL = "https://chatgpt.com/backend-api"

	apiEndpoint   = "/v1/responses"
	codexEndpoint = "/codex/responses"

	// Codex client identity, required for OAuth requests to chatgpt.com.
	// Newer models are gated on the first-party Codex identity; a neutral
	// version is used so we don't claim a specific Codex release.
	codexOriginator = "codex_cli_rs"
	codexUserAgent  = "codex_cli_rs/0.0.0 (Moa)"
)

// OpenAI implements core.Provider for the OpenAI Responses API.
type OpenAI struct {
	apiKey    string
	baseURL   string
	endpoint  string
	accountID string // ChatGPT OAuth account ID (empty for API key auth)
	// oauth marks the ChatGPT subscription transport. Reactive token refresh
	// must only ever apply there: API-key credentials are not refreshable and
	// a 401 on them is always terminal.
	oauth        bool
	refreshOAuth func(rejectedToken string) (string, error)
	client       *http.Client
}

// New creates an OpenAI provider using an API key (api.openai.com).
func New(apiKey string) *OpenAI {
	return &OpenAI{
		apiKey:   apiKey,
		baseURL:  apiBaseURL,
		endpoint: apiEndpoint,
		client:   &http.Client{Timeout: 10 * time.Minute},
	}
}

// NewOAuth creates an OpenAI provider using ChatGPT subscription OAuth.
// Uses chatgpt.com/backend-api with the /codex/responses endpoint.
func NewOAuth(accessToken, accountID string, refresh func(rejectedToken string) (string, error)) *OpenAI {
	return &OpenAI{
		apiKey:       accessToken,
		baseURL:      codexBaseURL,
		endpoint:     codexEndpoint,
		accountID:    accountID,
		oauth:        true,
		refreshOAuth: refresh,
		client:       &http.Client{Timeout: 10 * time.Minute},
	}
}

// NewWithBaseURL creates an OpenAI provider with a custom base URL (for testing).
func NewWithBaseURL(apiKey, baseURL string) *OpenAI {
	return &OpenAI{
		apiKey:   apiKey,
		baseURL:  baseURL,
		endpoint: apiEndpoint,
		client:   &http.Client{Timeout: 10 * time.Minute},
	}
}

// SupportsDocuments returns true only for the API-key path (/v1/responses):
// the codex OAuth path (/codex/responses) is unverified for input_file.
func (o *OpenAI) SupportsDocuments() bool { return o.endpoint == apiEndpoint }

// supportsMaxOutputTokens reports whether the active endpoint accepts the
// max_output_tokens parameter. The public Responses API (/v1/responses) does;
// the ChatGPT OAuth backend (/codex/responses) rejects it with HTTP 400
// ("Unsupported parameter: max_output_tokens").
func (o *OpenAI) supportsMaxOutputTokens() bool { return o.endpoint == apiEndpoint }

// supportsExplicitCacheBreakpoints is true only on the public Responses API
// for GPT-5.6+. The ChatGPT OAuth backend has not been shown to accept
// prompt_cache_options or prompt_cache_breakpoint; the official Codex client
// does not send them.
func (o *OpenAI) supportsExplicitCacheBreakpoints(modelID string) bool {
	return o.endpoint == apiEndpoint && modelSupportsExplicitCacheBreakpoints(modelID)
}

// Stream sends a request and returns a channel of normalized AssistantEvents.
func (o *OpenAI) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	// GPT-6 Astra rejects temperature. The normal agent path does not set it,
	// but clearing it here also keeps direct provider callers on the documented
	// contract without changing other OpenAI models.
	if strings.EqualFold(req.Model.ID, "gpt-6-astra") {
		req.Options.Temperature = nil
	}

	apiKey := o.apiKey
	if req.Options.APIKey != "" {
		apiKey = req.Options.APIKey
	}

	// prompt_cache_key is accepted by both transports: the official Codex
	// client sends it to the same ChatGPT OAuth backend, unlike
	// max_output_tokens which that backend rejects.
	//
	// Explicit breakpoints are the opposite: Codex's ResponsesApiRequest has
	// prompt_cache_key but not prompt_cache_options / prompt_cache_breakpoint,
	// and /codex/responses 400s on unsupported fields. Keep them on the
	// public API only.
	dialect := responses.Dialect{
		Provider:                         "openai",
		Model:                            req.Model.ID,
		SupportsDocuments:                o.SupportsDocuments(),
		SupportsMaxOutputTokens:          o.supportsMaxOutputTokens(),
		SupportsParallelToolCalls:        o.endpoint == apiEndpoint,
		SupportsPromptCacheKey:           true,
		SupportsExplicitCacheBreakpoints: o.supportsExplicitCacheBreakpoints(req.Model.ID),
		// The Codex transport rejects service_tier outright; only the public
		// API prices a priority tier.
		SupportsServiceTier:     o.endpoint == apiEndpoint,
		AllowedReasoningEfforts: core.ReasoningEffortsForModel(req.Model),
	}
	body, err := responses.BuildRequestBody(req, dialect)
	if err != nil {
		return nil, fmt.Errorf("openai: building request: %w", err)
	}

	request := func(token string) func() (*http.Request, error) {
		return func() (*http.Request, error) {
			r, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+o.endpoint, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Accept", "text/event-stream")
			if o.accountID != "" {
				r.Header.Set("chatgpt-account-id", o.accountID)
				// Newer Codex models (e.g. gpt-5.6-luna) are gated on the
				// first-party Codex client identity: without these headers the
				// backend returns "Model not found". A neutral version is used so
				// we don't claim a specific Codex release.
				r.Header.Set("originator", codexOriginator)
				r.Header.Set("User-Agent", codexUserAgent)
			}
			return r, nil
		}
	}

	// Don't burn retries on a usage-limit 429 — the limit won't clear for
	// hours, and we want the response back so we can build a typed quota error.
	policy := retry.DefaultPolicy
	policy.Retryable = func(resp *http.Response, body []byte) bool {
		if resp.StatusCode == http.StatusTooManyRequests && isUsageLimitBody(body) {
			return false // terminal — return to caller, don't retry
		}
		return true
	}
	resp, err := retry.Do(ctx, o.client, request(apiKey), policy, nil)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}

	// The ChatGPT backend can reject an OAuth access token long before the
	// expiry the token itself advertises (observed as 401 "token_expired" with
	// days of declared lifetime left), so the store's proactive expiry check
	// never fires. Refresh exactly once and rebuild the request with the fresh
	// token; a second 401 is terminal. API-key credentials never take this
	// path — they cannot be refreshed and their 401s mean something else.
	if resp.StatusCode == http.StatusUnauthorized && o.oauth && o.refreshOAuth != nil {
		resp.Body.Close() //nolint:errcheck
		fresh, refreshErr := o.refreshOAuth(apiKey)
		if refreshErr != nil {
			return nil, fmt.Errorf("openai: authentication failed (run --login openai to re-authenticate)")
		}
		apiKey = fresh
		resp, err = retry.Do(ctx, o.client, request(apiKey), policy, nil)
		if err != nil {
			return nil, fmt.Errorf("openai: %w", err)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close() //nolint:errcheck
			return nil, fmt.Errorf("openai: authentication failed (run --login openai to re-authenticate)")
		}
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close() //nolint:errcheck
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// Usage-limit exhaustion is a distinct, actionable condition (not a
		// generic error and not a user interruption): surface it typed so the
		// UI can show "limit reached, resets in X".
		if resp.StatusCode == http.StatusTooManyRequests && isUsageLimitBody(errBody) {
			return nil, quotaErrorFrom(resp, errBody)
		}
		return nil, fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, string(errBody))
	}

	ch := make(chan core.AssistantEvent, 64)
	// Only the model ID is needed to consume the stream. Capturing req itself
	// would keep the whole request — every image in base64 — reachable for as
	// long as the SSE body is being consumed.
	modelID := req.Model.ID
	requestedPriority := responses.RequestsPriority(req, dialect)

	go func() {
		defer resp.Body.Close() //nolint:errcheck
		defer close(ch)
		// ChatGPT/Codex returns plan rate-limit state in the response headers
		// (x-codex-*), available now, before the SSE body. Emit it up front as
		// its own event so callers get instant per-request quota awareness — the
		// OpenAI counterpart to Anthropic's rate-limit headers. Only the OAuth
		// (Codex) backend sends these; api.openai.com (API key) does not, and
		// parseRateLimit returns nil there, so nothing is emitted.
		if o.accountID != "" {
			if rl := parseRateLimit(resp.Header); rl != nil {
				ch <- core.AssistantEvent{Type: core.ProviderEventRateLimit, RateLimit: rl}
			}
		}
		body := io.Reader(sseutil.NewIdleTimeoutReader(resp.Body, 5*time.Minute))
		responses.ConsumeStreamWithPriority(ctx, body, ch, "openai", modelID, requestedPriority)
	}()

	return ch, nil
}
