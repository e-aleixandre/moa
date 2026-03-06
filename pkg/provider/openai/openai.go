// Package openai implements core.Provider for the OpenAI Chat Completions API.
// Supports GPT and Codex models with streaming, tool use, and reasoning effort.
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

// OpenAI implements core.Provider for the OpenAI Chat Completions API.
type OpenAI struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// New creates an OpenAI provider.
func New(apiKey string) *OpenAI {
	return &OpenAI{
		apiKey:  apiKey,
		baseURL: "https://api.openai.com",
		client:  &http.Client{Timeout: 10 * time.Minute},
	}
}

// NewWithBaseURL creates an OpenAI provider with a custom base URL (for testing).
func NewWithBaseURL(apiKey, baseURL string) *OpenAI {
	o := New(apiKey)
	o.baseURL = baseURL
	return o
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

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

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
