// Package openai implements core.Provider for the OpenAI Responses API.
// Supports GPT and Codex models with streaming, tool use, and reasoning effort.
// Works with both API keys (api.openai.com) and OAuth (chatgpt.com/backend-api).
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
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
	refreshOAuth func(ctx context.Context, rejectedToken string) (string, error)
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
func NewOAuth(accessToken, accountID string, refresh func(ctx context.Context, rejectedToken string) (string, error)) *OpenAI {
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

// WithHTTPClient replaces the HTTP client while keeping the pinned endpoint.
// It is a server-side seam for tests that route the fixed origin locally.
func (o *OpenAI) WithHTTPClient(c *http.Client) *OpenAI {
	o.client = c
	return o
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

// SupportsDeferredTools reports whether modelID loads deferred tools through
// tool_search: on the plan transport, where it was probed for every model the
// plan serves (GPT-5.4 and later), unless a request was already refused for
// it (see Stream). The paid API was never probed and keeps the full list.
func (o *OpenAI) SupportsDeferredTools(modelID string) bool {
	if !o.oauth || o.endpoint != codexEndpoint {
		return false
	}
	if _, refused := toolSearchRefused.Load(toolSearchKey(o.baseURL, modelID)); refused {
		return false
	}
	return modelSupportsToolSearch(modelID)
}

// modelSupportsToolSearch is true from GPT-5.4 on, the first release with
// tool_search, and for the Daybreak alias.
func modelSupportsToolSearch(modelID string) bool {
	id := strings.ToLower(modelID)
	if strings.HasPrefix(id, "gpt-daybreak") {
		return true
	}
	rest, ok := strings.CutPrefix(id, "gpt-")
	if !ok {
		return false
	}
	major, minor, ok := parseLeadingVersion(rest)
	return ok && (major > 5 || major == 5 && minor >= 4)
}

// toolSearchRefused remembers, for the life of the process, the endpoint and
// model combinations that refused tool search, so their later requests send
// the full list straight away. Providers are built per request, so this
// cannot live on one.
var toolSearchRefused sync.Map

func toolSearchKey(baseURL, modelID string) string {
	return baseURL + "|" + modelID
}

func hasDeferred(specs []core.ToolSpec) bool {
	for _, s := range specs {
		if s.Deferred {
			return true
		}
	}
	return false
}

// withoutDeferral is req with every tool loaded up front: tool_search is then
// an ordinary function and its results go as text.
func withoutDeferral(req core.Request) core.Request {
	tools := make([]core.ToolSpec, len(req.Tools))
	for i, s := range req.Tools {
		s.Deferred = false
		tools[i] = s
	}
	req.Tools = tools
	return req
}

// Stream sends a request and returns a channel of normalized AssistantEvents.
func (o *OpenAI) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	// The fallback without tool search resends the request as the caller
	// made it: the adjustments below are not idempotent.
	original := req
	// GPT-6 Astra rejects temperature. The normal agent path does not set it,
	// but clearing it here also keeps direct provider callers on the documented
	// contract without changing other OpenAI models.
	if strings.EqualFold(req.Model.ID, "gpt-6-astra") {
		req.Options.Temperature = nil
	}

	// Daybreak travels only on the plan transport, the one the official
	// client sends it on. It gets its own cache routing there: the body key
	// and the session-id header below both derive from it.
	if o.oauth && req.Options.AccessProgram != "" {
		req.Options.PromptCacheKey = core.DaybreakPromptCacheKey(req.Options.PromptCacheKey, req.Options.AccessProgram)
	} else {
		req.Options.AccessProgram = ""
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
		SupportsAccessPrograms:  o.oauth,
		SupportsToolSearch:      o.SupportsDeferredTools(req.Model.ID),
		AllowedReasoningEfforts: core.ReasoningEffortsForModel(req.Model),
	}
	body, err := responses.BuildRequestBody(req, dialect)
	if err != nil {
		return nil, fmt.Errorf("openai: building request: %w", err)
	}
	searching := dialect.SupportsToolSearch && hasDeferred(req.Tools)

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
				// The ChatGPT backend groups a conversation's prompt cache by
				// this header, not by the body's prompt_cache_key. Without it
				// every request looks like a fresh session: the prefix is
				// written to cache each turn and never read back, which costs
				// more than not caching at all (a write is billed above plain
				// input). Measured live against /codex/responses with a
				// byte-identical body, only this header differing:
				// gpt-daybreak-blue-latest went from cached=0/write=16102 to
				// cached=16102 on the repeat call, gpt-5.6-sol from cached=0 to
				// cached=15872.
				//
				// The value only has to be stable per conversation — the
				// backend accepts any string, not just a UUID — so the routing
				// key the request already carries doubles as the session id and
				// keeps sibling subagents grouped exactly as it does in-body.
				if req.Options.PromptCacheKey != "" {
					r.Header.Set("session-id", req.Options.PromptCacheKey)
				}
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
		fresh, refreshErr := o.refreshOAuth(ctx, apiKey)
		if refreshErr != nil {
			return nil, core.ReactiveRefreshError("openai", refreshErr)
		}
		apiKey = fresh
		resp, err = retry.Do(ctx, o.client, request(apiKey), policy, nil)
		if err != nil {
			return nil, fmt.Errorf("openai: %w", err)
		}
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close() //nolint:errcheck
		// A rejected credential is reported by status only: the body of an
		// auth failure can echo what was sent.
		if resp.StatusCode == http.StatusForbidden && req.Options.AccessProgram != "" {
			// A model that does not take Daybreak is refused with a 403 whose
			// detail is the useful part. Only that fixed message is surfaced:
			// any other 403 stays a credential error, reported by status.
			body, _ := retry.ErrorBody(resp.Body)
			if detail := daybreakRefusal(body); detail != "" {
				return nil, fmt.Errorf("openai: %s", detail)
			}
			return nil, &core.ProviderAuthError{Provider: "openai", Status: resp.StatusCode}
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, &core.ProviderAuthError{Provider: "openai", Status: resp.StatusCode}
		}
		errBody, errText := retry.ErrorBody(resp.Body)
		// Usage-limit exhaustion is a distinct, actionable condition (not a
		// generic error and not a user interruption): surface it typed so the
		// UI can show "limit reached, resets in X".
		if resp.StatusCode == http.StatusTooManyRequests && isUsageLimitBody(errBody) {
			return nil, quotaErrorFrom(resp, errBody)
		}
		// A model or backend that does not take tool search refuses the
		// request outright. The same request with every tool loaded keeps the
		// session going; only if that one is accepted was tool search the
		// cause, and the combination is remembered so later requests skip
		// the round trip.
		refusal := resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity
		if refusal && searching {
			ch, err := o.Stream(ctx, withoutDeferral(original))
			if err == nil {
				toolSearchRefused.Store(toolSearchKey(o.baseURL, req.Model.ID), true)
				// No body: an upstream error can echo what was sent.
				slog.Warn("openai: tool search refused; sending the full tool list", "model", req.Model.ID, "status", resp.StatusCode)
			}
			return ch, err
		}
		return nil, fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, errText)
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
				ch <- core.AssistantEvent{Type: core.ProviderEventRateLimit, RateLimit: rl, Provider: "openai"}
			}
		}
		body := io.Reader(sseutil.NewIdleTimeoutReader(resp.Body, 5*time.Minute))
		responses.ConsumeStreamWithPriority(ctx, body, ch, "openai", modelID, requestedPriority)
	}()

	return ch, nil
}

// daybreakRefusal returns the plan backend's explanation for refusing a
// Daybreak request ({"detail":"Daybreak isn't available for this model…"}),
// or "" when body is anything else.
func daybreakRefusal(body []byte) string {
	var payload struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &payload) != nil || !strings.Contains(payload.Detail, "Daybreak") {
		return ""
	}
	return payload.Detail
}
