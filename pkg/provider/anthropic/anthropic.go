package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
)

// Anthropic implements core.Provider for the Anthropic Messages API.
// Supports both API key auth and OAuth tokens (Claude Max).
type Anthropic struct {
	apiKey  string
	isOAuth bool   // true if apiKey is an OAuth token (sk-ant-oat-...)
	baseURL string
	client  *http.Client
}

// New creates an Anthropic provider.
// Automatically detects OAuth tokens by their "sk-ant-oat" prefix.
func New(apiKey string) *Anthropic {
	return &Anthropic{
		apiKey:  apiKey,
		isOAuth: isOAuthToken(apiKey),
		baseURL: "https://api.anthropic.com",
		client:  &http.Client{Timeout: 10 * time.Minute},
	}
}

// NewWithBaseURL creates an Anthropic provider with a custom base URL (for testing).
func NewWithBaseURL(apiKey, baseURL string) *Anthropic {
	a := New(apiKey)
	a.baseURL = baseURL
	return a
}

// IsOAuth returns true if this provider is using an OAuth token.
func (a *Anthropic) IsOAuth() bool {
	return a.isOAuth
}

// isOAuthToken returns true if the key is an Anthropic OAuth token.
func isOAuthToken(key string) bool {
	return strings.HasPrefix(key, "sk-ant-oat")
}

// Stream sends a request to the Anthropic Messages API and returns a channel
// of normalized AssistantEvents.
//
// Error contract:
//   - Returns error for pre-stream failures (bad request, auth, network).
//   - If channel is returned, exactly one terminal event ("done" or "error")
//     will be sent before the channel is closed.
func (a *Anthropic) Stream(ctx context.Context, req core.Request) (<-chan core.AssistantEvent, error) {
	// Override API key if provided in options — recompute OAuth mode
	apiKey := a.apiKey
	oauthMode := a.isOAuth
	if req.Options.APIKey != "" {
		apiKey = req.Options.APIKey
		oauthMode = isOAuthToken(apiKey)
	}

	body, err := buildRequestBody(req, oauthMode)
	if err != nil {
		return nil, fmt.Errorf("anthropic: building request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	if oauthMode {
		// OAuth: Bearer auth + Claude Code identity headers (required by Anthropic)
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		httpReq.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14,interleaved-thinking-2025-05-14")
		httpReq.Header.Set("User-Agent", "claude-cli/"+claudeCodeVersion)
		httpReq.Header.Set("x-app", "cli")
	} else {
		// Standard API key auth
		httpReq.Header.Set("X-API-Key", apiKey)
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: http: %w", err)
	}

	// Check HTTP status BEFORE returning channel
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("anthropic: HTTP %d: %s", resp.StatusCode, string(errBody))
	}

	ch := make(chan core.AssistantEvent, 64)

	go func() {
		defer resp.Body.Close()
		defer close(ch)
		a.consumeStream(ctx, resp.Body, ch, req.Tools, oauthMode)
	}()

	return ch, nil
}

// consumeStream parses SSE frames and emits normalized events.
// Guarantees exactly one terminal event ("done" or "error") before returning.
func (a *Anthropic) consumeStream(ctx context.Context, body io.Reader, ch chan<- core.AssistantEvent, tools []core.ToolSpec, oauthMode bool) {
	state := &streamState{requestTools: tools, isOAuth: oauthMode}
	sentTerminal := false

	defer func() {
		if !sentTerminal {
			// Unexpected exit without terminal event
			ch <- core.AssistantEvent{
				Type:  core.ProviderEventError,
				Error: fmt.Errorf("stream ended without terminal event"),
			}
		}
	}()

	err := parseSSEFrames(body, func(eventType, data string) {
		// Check context cancellation
		if ctx.Err() != nil {
			return
		}

		event := a.mapEvent(eventType, data, state)
		if event == nil {
			return
		}

		ch <- *event

		if event.IsTerminal() {
			sentTerminal = true
		}
	})

	if err != nil && !sentTerminal {
		if ctx.Err() != nil {
			ch <- core.AssistantEvent{
				Type:  core.ProviderEventError,
				Error: ctx.Err(),
			}
		} else {
			ch <- core.AssistantEvent{
				Type:  core.ProviderEventError,
				Error: fmt.Errorf("SSE parse: %w", err),
			}
		}
		sentTerminal = true
	}
}

// streamState tracks the evolving message across SSE events.
type streamState struct {
	message      core.Message
	contentIdx   int
	blockType    string // current block type being built
	jsonAccum    string // accumulated JSON for tool_use input
	toolCallID   string
	toolCallName string
	requestTools []core.ToolSpec // original tool specs for reverse name mapping
	isOAuth      bool           // whether this request used OAuth (for tool name mapping)
}

// mapEvent converts an Anthropic SSE event to a normalized AssistantEvent.
func (a *Anthropic) mapEvent(eventType, data string, state *streamState) *core.AssistantEvent {
	switch eventType {
	case "message_start":
		return a.handleMessageStart(data, state)
	case "content_block_start":
		return a.handleContentBlockStart(data, state)
	case "content_block_delta":
		return a.handleContentBlockDelta(data, state)
	case "content_block_stop":
		return a.handleContentBlockStop(state)
	case "message_delta":
		return a.handleMessageDelta(data, state)
	case "message_stop":
		return a.handleMessageStop(state)
	case "error":
		return a.handleError(data)
	case "ping":
		return nil // keep-alive
	default:
		return nil
	}
}

func (a *Anthropic) handleError(data string) *core.AssistantEvent {
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return &core.AssistantEvent{
			Type:  core.ProviderEventError,
			Error: fmt.Errorf("anthropic error (unparseable): %.200s", data),
		}
	}
	return &core.AssistantEvent{
		Type:  core.ProviderEventError,
		Error: fmt.Errorf("anthropic %s: %s", payload.Error.Type, payload.Error.Message),
	}
}

func (a *Anthropic) handleMessageStart(data string, state *streamState) *core.AssistantEvent {
	var payload struct {
		Message struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
				CacheRead    int `json:"cache_read_input_tokens"`
				CacheCreate  int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return &core.AssistantEvent{
			Type:  core.ProviderEventError,
			Error: fmt.Errorf("parse message_start: %w (data: %.100s)", err, data),
		}
	}

	state.message = core.Message{
		Role:     "assistant",
		Provider: "anthropic",
		Model:    payload.Message.Model,
		Usage: &core.Usage{
			Input:      payload.Message.Usage.InputTokens,
			Output:     payload.Message.Usage.OutputTokens,
			CacheRead:  payload.Message.Usage.CacheRead,
			CacheWrite: payload.Message.Usage.CacheCreate,
		},
		Timestamp: time.Now().Unix(),
	}

	partial := state.message // copy
	return &core.AssistantEvent{
		Type:    core.ProviderEventStart,
		Partial: &partial,
	}
}

func (a *Anthropic) handleContentBlockStart(data string, state *streamState) *core.AssistantEvent {
	var payload struct {
		Index        int `json:"index"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id,omitempty"`
			Name string `json:"name,omitempty"`
		} `json:"content_block"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return &core.AssistantEvent{
			Type:  core.ProviderEventError,
			Error: fmt.Errorf("parse content_block_start: %w (data: %.100s)", err, data),
		}
	}

	state.contentIdx = payload.Index
	state.blockType = payload.ContentBlock.Type

	switch payload.ContentBlock.Type {
	case "text":
		state.message.Content = append(state.message.Content, core.TextContent(""))
		return &core.AssistantEvent{
			Type:         core.ProviderEventTextStart,
			ContentIndex: payload.Index,
		}

	case "thinking":
		state.message.Content = append(state.message.Content, core.ThinkingContent(""))
		return &core.AssistantEvent{
			Type:         core.ProviderEventThinkingStart,
			ContentIndex: payload.Index,
		}

	case "tool_use":
		toolName := payload.ContentBlock.Name
		if state.isOAuth {
			// Map CC-cased names back to our original tool names
			toolName = fromClaudeCodeName(toolName, state.requestTools)
		}
		state.toolCallID = payload.ContentBlock.ID
		state.toolCallName = toolName
		state.jsonAccum = ""
		state.message.Content = append(state.message.Content, core.ToolCallContent(
			payload.ContentBlock.ID,
			toolName,
			nil,
		))
		return &core.AssistantEvent{
			Type:         core.ProviderEventToolCallStart,
			ContentIndex: payload.Index,
		}

	default:
		return nil
	}
}

func (a *Anthropic) handleContentBlockDelta(data string, state *streamState) *core.AssistantEvent {
	var payload struct {
		Index int `json:"index"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text,omitempty"`
			Thinking    string `json:"thinking,omitempty"`
			Signature   string `json:"signature,omitempty"`
			PartialJSON string `json:"partial_json,omitempty"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return &core.AssistantEvent{
			Type:  core.ProviderEventError,
			Error: fmt.Errorf("parse content_block_delta: %w (data: %.100s)", err, data),
		}
	}

	idx := payload.Index

	switch payload.Delta.Type {
	case "text_delta":
		// Append text to current content block
		if idx < len(state.message.Content) {
			state.message.Content[idx].Text += payload.Delta.Text
		}
		return &core.AssistantEvent{
			Type:         core.ProviderEventTextDelta,
			ContentIndex: idx,
			Delta:        payload.Delta.Text,
		}

	case "thinking_delta":
		if idx < len(state.message.Content) {
			state.message.Content[idx].Thinking += payload.Delta.Thinking
		}
		return &core.AssistantEvent{
			Type:         core.ProviderEventThinkingDelta,
			ContentIndex: idx,
			Delta:        payload.Delta.Thinking,
		}

	case "signature_delta":
		// Thinking block signature — required for multi-turn with thinking.
		// Must be preserved unmodified in message history.
		if idx < len(state.message.Content) {
			state.message.Content[idx].ThinkingSignature += payload.Delta.Signature
		}
		return nil // No user-visible event for signatures

	case "input_json_delta":
		state.jsonAccum += payload.Delta.PartialJSON
		return &core.AssistantEvent{
			Type:         core.ProviderEventToolCallDelta,
			ContentIndex: idx,
			Delta:        payload.Delta.PartialJSON,
		}

	default:
		return nil
	}
}

func (a *Anthropic) handleContentBlockStop(state *streamState) *core.AssistantEvent {
	idx := state.contentIdx

	switch state.blockType {
	case "text":
		return &core.AssistantEvent{
			Type:         core.ProviderEventTextEnd,
			ContentIndex: idx,
		}

	case "thinking":
		return &core.AssistantEvent{
			Type:         core.ProviderEventThinkingEnd,
			ContentIndex: idx,
		}

	case "tool_use":
		// Parse accumulated JSON into arguments
		if idx < len(state.message.Content) && state.jsonAccum != "" {
			var args map[string]any
			if err := json.Unmarshal([]byte(state.jsonAccum), &args); err == nil {
				state.message.Content[idx].Arguments = args
			}
		}
		state.jsonAccum = ""
		return &core.AssistantEvent{
			Type:         core.ProviderEventToolCallEnd,
			ContentIndex: idx,
		}

	default:
		return nil
	}
}

func (a *Anthropic) handleMessageDelta(data string, state *streamState) *core.AssistantEvent {
	var payload struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return &core.AssistantEvent{
			Type:  core.ProviderEventError,
			Error: fmt.Errorf("parse message_delta: %w (data: %.100s)", err, data),
		}
	}

	state.message.StopReason = payload.Delta.StopReason
	if state.message.Usage != nil {
		state.message.Usage.Output = payload.Usage.OutputTokens
		state.message.Usage.TotalTokens = state.message.Usage.Input +
			state.message.Usage.Output +
			state.message.Usage.CacheRead +
			state.message.Usage.CacheWrite
	}

	return nil // No normalized event for message_delta; info captured in state
}

func (a *Anthropic) handleMessageStop(state *streamState) *core.AssistantEvent {
	final := state.message // copy
	return &core.AssistantEvent{
		Type:    core.ProviderEventDone,
		Message: &final,
	}
}
