// Package meta implements the Meta Model API (Muse Spark) transport.
package meta

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/provider/responses"
	"github.com/e-aleixandre/moa/pkg/provider/retry"
	"github.com/e-aleixandre/moa/pkg/provider/sseutil"
)

const (
	apiBaseURL  = "https://api.meta.ai"
	apiEndpoint = "/v1/responses"
)

// Meta is the Meta Model API provider. Both credential kinds talk to
// api.meta.ai with a bearer key: a subscription login differs only in that its
// key is minted from an OAuth token and can be re-minted when rejected.
type Meta struct {
	apiKey  string
	remint  func(rejectedKey string) (string, error)
	client  *http.Client
	baseURL string
}

// New creates a Meta provider for META_API_KEY credentials.
func New(apiKey string) *Meta {
	return &Meta{apiKey: apiKey, baseURL: apiBaseURL, client: &http.Client{Timeout: 10 * time.Minute}}
}

// NewOAuth creates a Meta provider for a Muse subscription. The key is the one
// minted at login; remint exchanges a rejected key for a fresh one.
func NewOAuth(mintedKey string, remint func(rejectedKey string) (string, error)) *Meta {
	p := New(mintedKey)
	p.remint = remint
	return p
}

// SupportsDocuments is true: input_file parts carrying a base64 data URL were
// verified against /v1/responses, and the model read the attached file.
func (*Meta) SupportsDocuments() bool { return true }

// Stream sends a stateless request to Meta's Responses endpoint.
func (m *Meta) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	apiKey := m.apiKey
	if req.Options.APIKey != "" {
		apiKey = req.Options.APIKey
	}
	thinking, err := normalizeThinking(req.Options.ThinkingLevel)
	if err != nil {
		return nil, fmt.Errorf("meta: invalid thinking level %q (choose: minimal, low, medium, high, xhigh)", req.Options.ThinkingLevel)
	}
	req.Options.ThinkingLevel = thinking
	dialect := responses.Dialect{
		Provider: "meta", Model: req.Model.ID,
		SupportsDocuments: true, SupportsMaxOutputTokens: true, SupportsParallelToolCalls: true,
		// Verified live: repeating a long prompt under the same
		// prompt_cache_key reported cached_tokens, and the same prefix under a
		// different key did not. service_tier is echoed back as "auto"
		// whatever is sent, so fast mode has nothing to buy here.
		SupportsPromptCacheKey:  true,
		SupportsServiceTier:     false,
		AllowedReasoningEfforts: []string{"minimal", "low", "medium", "high", "xhigh"},
	}
	body, err := responses.BuildRequestBody(req, dialect)
	if err != nil {
		return nil, fmt.Errorf("meta: building request: %w", err)
	}

	request := func(token string) func() (*http.Request, error) {
		return func() (*http.Request, error) {
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+apiEndpoint, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Accept", "text/event-stream")
			return r, nil
		}
	}
	policy := retry.DefaultPolicy
	policy.Retryable = func(resp *http.Response, body []byte) bool {
		return resp.StatusCode != http.StatusTooManyRequests || !isQuotaBody(body)
	}
	resp, err := retry.Do(ctx, m.client, request(apiKey), policy, nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// retry.Do includes the last upstream body in exhaustion errors; never
		// surface it through this boundary.
		return nil, fmt.Errorf("meta: request failed after retries")
	}
	// A minted subscription key can be revoked or expire; Muse Code re-mints
	// instead of re-authenticating. Try exactly once, and never for a plain
	// API key.
	if resp.StatusCode == http.StatusUnauthorized && m.remint != nil {
		resp.Body.Close() //nolint:errcheck
		fresh, remintErr := m.remint(apiKey)
		if remintErr != nil {
			return nil, fmt.Errorf("meta: authentication failed")
		}
		apiKey = fresh
		resp, err = retry.Do(ctx, m.client, request(apiKey), policy, nil)
		if err != nil {
			return nil, fmt.Errorf("meta: request failed after retries")
		}
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close() //nolint:errcheck
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, classifyHTTPResponse(resp, errBody)
	}
	ch := make(chan core.AssistantEvent, 64)
	// Only the model ID is needed to consume the stream; capturing req would
	// keep every base64 image alive for as long as the body is read.
	modelID := req.Model.ID

	go func() {
		defer resp.Body.Close() //nolint:errcheck
		defer close(ch)
		responses.ConsumeStreamSanitized(ctx, sseutil.NewIdleTimeoutReader(resp.Body, 5*time.Minute), ch, "meta", modelID)
	}()
	return ch, nil
}

// normalizeThinking translates Moa's no-reasoning internal calls to Muse
// Spark's lowest effort. The API rejects "none" ("reasoning.effort does not
// support none with this model"), so reasoning cannot be turned off.
func normalizeThinking(level string) (string, error) {
	if level == "" || level == "off" {
		return "minimal", nil
	}
	if !validThinking(level) {
		return "", fmt.Errorf("unsupported thinking level")
	}
	return level, nil
}

func validThinking(level string) bool {
	switch level {
	case "minimal", "low", "medium", "high", "xhigh":
		return true
	}
	return false
}

func classifyHTTPResponse(resp *http.Response, body []byte) error {
	return classifyHTTP(resp.StatusCode, body, resp.Header)
}

func classifyHTTP(status int, body []byte, headers http.Header) error {
	// Upstream bodies can carry request IDs and account details; keep the
	// messages stable and actionable instead.
	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("meta: authentication failed")
	case http.StatusForbidden:
		return fmt.Errorf("meta: subscription entitlement denied")
	case http.StatusNotFound:
		return fmt.Errorf("meta: model or endpoint not found")
	case http.StatusTooManyRequests:
		if isQuotaBody(body) {
			quota := &core.QuotaExceededError{Provider: "meta", Message: "API quota exceeded"}
			if seconds, err := strconv.Atoi(headers.Get("Retry-After")); err == nil && seconds > 0 {
				quota.ResetsIn = time.Duration(seconds) * time.Second
			}
			return quota
		}
		return fmt.Errorf("meta: rate limited")
	default:
		if status >= 500 {
			return fmt.Errorf("meta: service temporarily unavailable (HTTP %d)", status)
		}
		return fmt.Errorf("meta: request failed (HTTP %d)", status)
	}
}

// isQuotaBody separates an exhausted account from the per-minute throttling
// Meta also reports as 429 (its "output token rate limit" reserves capacity
// for max_output_tokens and clears on its own).
func isQuotaBody(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "quota") || strings.Contains(s, "usage limit") || strings.Contains(s, "credit")
}
