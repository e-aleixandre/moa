// Package xai implements the public xAI Responses API transport.
package xai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/responses"
	"github.com/e-aleixandre/moa/pkg/provider/retry"
	"github.com/e-aleixandre/moa/pkg/provider/sseutil"
)

const (
	apiBaseURL       = "https://api.x.ai"
	consumerBaseURL  = "https://cli-chat-proxy.grok.com"
	apiEndpoint      = "/v1/responses"
	consumerEndpoint = "/v1/responses"

	// grokBuildCompatVersion is the Grok Build contract version verified by
	// this adapter. The proxy uses it for client-version gating.
	grokBuildCompatVersion = "0.2.119"
)

// ConsumerBaseURL is the pinned Grok consumer proxy. It is exported for
// read-only integrations such as plan usage; API-key traffic must not use it.
const ConsumerBaseURL = consumerBaseURL

// XAI is the xAI public API-key provider. It deliberately has no custom-base
// URL constructor: alternate routes and credentials are distinct xAI products,
// not OpenAI-compatible configuration.
type XAI struct {
	apiKey       string
	baseURL      string
	endpoint     string
	consumer     bool
	refreshOAuth func(rejectedToken string) (string, error)
	client       *http.Client
}

// New creates an xAI provider for XAI_API_KEY credentials.
func New(apiKey string) *XAI {
	return &XAI{apiKey: apiKey, baseURL: apiBaseURL, endpoint: apiEndpoint, client: &http.Client{Timeout: 10 * time.Minute}}
}

// NewOAuth creates an xAI consumer provider. Consumer OAuth is intentionally
// pinned to the Grok CLI proxy; it must never fall back to api.x.ai.
func NewOAuth(accessToken string, refresh func(rejectedToken string) (string, error)) *XAI {
	return &XAI{apiKey: accessToken, baseURL: consumerBaseURL, endpoint: consumerEndpoint, consumer: true, refreshOAuth: refresh, client: &http.Client{Timeout: 10 * time.Minute}}
}

// SupportsDocuments is false until xAI input_file support is verified. Images
// are Responses input_image parts and remain supported by the shared codec.
func (*XAI) SupportsDocuments() bool { return false }

// Stream sends a stateless request to xAI's public Responses endpoint.
func (x *XAI) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	apiKey := x.apiKey
	if req.Options.APIKey != "" {
		apiKey = req.Options.APIKey
	}
	thinking, err := normalizeThinking(req.Options.ThinkingLevel)
	if err != nil {
		return nil, fmt.Errorf("xai: invalid thinking level %q (choose: low, medium, high)", req.Options.ThinkingLevel)
	}
	req.Options.ThinkingLevel = thinking
	dialect := responses.Dialect{
		Provider: "xai", Model: req.Model.ID,
		SupportsDocuments: false, SupportsMaxOutputTokens: false, SupportsParallelToolCalls: true,
		// Both xAI transports accept prompt_cache_key: it is documented for
		// api.x.ai, and the consumer proxy was verified to accept it against
		// the live endpoint (HTTP 200, and cached_tokens came back non-zero).
		SupportsPromptCacheKey:  true,
		SupportsServiceTier:     true,
		AllowedReasoningEfforts: []string{"low", "medium", "high"},
	}
	body, err := responses.BuildRequestBody(req, dialect)
	if err != nil {
		return nil, fmt.Errorf("xai: building request: %w", err)
	}

	request := func(token string) func() (*http.Request, error) {
		return func() (*http.Request, error) {
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, x.baseURL+x.endpoint, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Accept", "text/event-stream")
			if x.consumer {
				setConsumerHeaders(r, token, "text/event-stream", "application/json")
			}
			return r, nil
		}
	}
	policy := retry.DefaultPolicy
	policy.Retryable = func(resp *http.Response, body []byte) bool {
		if resp.StatusCode == http.StatusTooManyRequests && isQuotaBody(body) {
			return false
		}
		return true
	}
	resp, err := retry.Do(ctx, x.client, request(apiKey), policy, nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// retry.Do includes the last upstream body in exhaustion errors. Never
		// expose it through xAI's public error boundary.
		return nil, fmt.Errorf("xai: request failed after retries")
	}
	// A consumer bearer token can be revoked before its advertised expiry.
	// Refresh exactly once, rebuilding the request and never applying this to
	// API-key credentials.
	if resp.StatusCode == http.StatusUnauthorized && x.consumer && x.refreshOAuth != nil {
		resp.Body.Close() //nolint:errcheck
		fresh, refreshErr := x.refreshOAuth(apiKey)
		if refreshErr != nil {
			return nil, fmt.Errorf("xai: authentication failed")
		}
		apiKey = fresh
		resp, err = retry.Do(ctx, x.client, request(apiKey), policy, nil)
		if err != nil {
			return nil, fmt.Errorf("xai: request failed after retries")
		}
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close() //nolint:errcheck
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, classifyHTTPResponse(resp, errBody)
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
		responses.ConsumeStreamSanitizedWithPriority(ctx, sseutil.NewIdleTimeoutReader(resp.Body, 5*time.Minute), ch, "xai", modelID, requestedPriority)
	}()
	return ch, nil
}

// normalizeThinking translates Moa's no-reasoning internal calls to Grok's
// lowest supported effort. Grok rejects empty and "off" values, while
// compaction, handoffs, titles, and legacy verification intentionally use
// those values to avoid spending a session's selected thinking budget.
func normalizeThinking(level string) (string, error) {
	if level == "" || level == "off" {
		return "low", nil
	}
	if !validThinking(level) {
		return "", fmt.Errorf("unsupported thinking level")
	}
	return level, nil
}

func validThinking(level string) bool { return level == "low" || level == "medium" || level == "high" }

// SetConsumerHeaders applies the compatibility contract required by the Grok
// consumer proxy. Keep all proxy callers on this single implementation.
func SetConsumerHeaders(r *http.Request, token, accept, contentType string) {
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
	r.Header.Set("x-authenticateresponse", "authenticate-response")
	r.Header.Set("x-grok-client-version", grokBuildCompatVersion)
	r.Header.Set("x-grok-client-identifier", "grok-shell")
	r.Header.Set("x-grok-client-mode", "interactive")
	r.Header.Set("User-Agent", fmt.Sprintf("Moa grok-shell/%s (%s; %s)", grokBuildCompatVersion, runtime.GOOS, runtime.GOARCH))
	r.Header.Set("Accept", accept)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
}

func setConsumerHeaders(r *http.Request, token, accept, contentType string) {
	SetConsumerHeaders(r, token, accept, contentType)
}

func isQuotaBody(body []byte) bool {
	var payload struct {
		Code    string `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
		Error   struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil {
		for _, value := range []string{payload.Code, payload.Type, payload.Error.Code, payload.Error.Type} {
			value = strings.ToLower(value)
			if strings.Contains(value, "quota") || strings.Contains(value, "usage_limit") || strings.Contains(value, "credit") {
				return true
			}
		}
		for _, message := range []string{payload.Message, payload.Error.Message} {
			message = strings.ToLower(message)
			if strings.Contains(message, "quota") || strings.Contains(message, "usage limit") || strings.Contains(message, "credit") {
				return true
			}
		}
		return false
	}
	s := strings.ToLower(string(body))
	return strings.Contains(s, "quota") || strings.Contains(s, "usage limit") || strings.Contains(s, "credit")
}

func classifyHTTPError(status int, body []byte) error {
	return classifyHTTP(status, body, nil)
}

func classifyHTTPResponse(resp *http.Response, body []byte) error {
	return classifyHTTP(resp.StatusCode, body, resp.Header)
}

func classifyHTTP(status int, body []byte, headers http.Header) error {
	// Never surface upstream bodies: they can include credentials, request IDs,
	// or provider implementation details. Keep messages stable and actionable.
	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("xai: authentication failed")
	case http.StatusForbidden:
		return fmt.Errorf("xai: subscription entitlement denied")
	case http.StatusNotFound:
		return fmt.Errorf("xai: model or endpoint not found")
	case http.StatusTooManyRequests:
		if isQuotaBody(body) {
			quota := &core.QuotaExceededError{Provider: "xai", Message: "API quota exceeded"}
			if seconds, err := strconv.Atoi(headers.Get("Retry-After")); err == nil && seconds > 0 {
				quota.ResetsIn = time.Duration(seconds) * time.Second
			}
			if reset, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil && reset > 0 {
				quota.ResetsAt = time.Unix(reset, 0)
				quota.ResetsIn = time.Until(quota.ResetsAt)
			}
			return quota
		}
		return fmt.Errorf("xai: rate limited")
	default:
		if status >= 500 {
			return fmt.Errorf("xai: service temporarily unavailable (HTTP %d)", status)
		}
		return fmt.Errorf("xai: request failed (HTTP %d)", status)
	}
}
