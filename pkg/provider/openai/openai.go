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
)

const (
	apiBaseURL   = "https://api.openai.com"
	codexBaseURL = "https://chatgpt.com/backend-api"

	apiEndpoint   = "/v1/responses"
	codexEndpoint = "/codex/responses"
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

// Stream sends a request and returns a channel of normalized AssistantEvents.
func (o *OpenAI) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	apiKey := o.apiKey
	if req.Options.APIKey != "" {
		apiKey = req.Options.APIKey
	}

	body, err := buildRequestBody(req)
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
		}
		return r, nil
	}

	resp, err := retry.Do(ctx, o.client, buildReq, retry.DefaultPolicy, nil)
	if err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close() //nolint:errcheck
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, string(errBody))
	}

	ch := make(chan core.AssistantEvent, 64)
	go func() {
		defer resp.Body.Close() //nolint:errcheck
		defer close(ch)
		consumeStream(ctx, resp.Body, ch)
	}()

	return ch, nil
}
