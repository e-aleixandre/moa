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
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/provider/retry"
	"github.com/ealeixandre/moa/pkg/provider/sseutil"
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
	client    *http.Client
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
func NewOAuth(accessToken, accountID string) *OpenAI {
	return &OpenAI{
		apiKey:    accessToken,
		baseURL:   codexBaseURL,
		endpoint:  codexEndpoint,
		accountID: accountID,
		client:    &http.Client{Timeout: 10 * time.Minute},
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

// Stream sends a request and returns a channel of normalized AssistantEvents.
func (o *OpenAI) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	apiKey := o.apiKey
	if req.Options.APIKey != "" {
		apiKey = req.Options.APIKey
	}

	body, err := buildRequestBody(req, o.SupportsDocuments(), o.supportsMaxOutputTokens())
	if err != nil {
		return nil, fmt.Errorf("openai: building request: %w", err)
	}

	buildReq := func() (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+o.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+apiKey)
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

	// Don't burn retries on a usage-limit 429 — the limit won't clear for
	// hours, and we want the response back so we can build a typed quota error.
	policy := retry.DefaultPolicy
	policy.Retryable = func(resp *http.Response, body []byte) bool {
		if resp.StatusCode == http.StatusTooManyRequests && isUsageLimitBody(body) {
			return false // terminal — return to caller, don't retry
		}
		return true
	}
	resp, err := retry.Do(ctx, o.client, buildReq, policy, nil)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
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
	go func() {
		defer resp.Body.Close() //nolint:errcheck
		defer close(ch)
		body := io.Reader(sseutil.NewIdleTimeoutReader(resp.Body, 5*time.Minute))
		consumeStream(ctx, body, ch)
	}()

	return ch, nil
}
