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

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+o.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")
	if o.accountID != "" {
		httpReq.Header.Set("chatgpt-account-id", o.accountID)
	}

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: http: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("openai: HTTP %d: %s", resp.StatusCode, string(errBody))
	}

	ch := make(chan core.AssistantEvent, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		consumeStream(ctx, resp.Body, ch)
	}()

	return ch, nil
}
