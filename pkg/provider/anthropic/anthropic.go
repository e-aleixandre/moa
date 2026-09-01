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

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/jsonutil"
	"github.com/e-aleixandre/moa/pkg/provider/retry"
	"github.com/e-aleixandre/moa/pkg/provider/sseutil"
)

// Anthropic implements core.Provider for the Anthropic Messages API.
// Supports both API key auth and OAuth tokens (Claude Max).
type Anthropic struct {
	apiKey  string
	isOAuth bool // true if apiKey is an OAuth token (sk-ant-oat-...)
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

// SupportsDocuments returns true: Anthropic document blocks are GA for both
// API-key and OAuth auth.
func (a *Anthropic) SupportsDocuments() bool { return true }

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

	fastMode := req.Options.Fast && core.SupportsFast(req.Model.ID)

	buildReq := func() (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("anthropic-version", "2023-06-01")
		if oauthMode {
			r.Header.Set("Authorization", "Bearer "+apiKey)
			betas := "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14,interleaved-thinking-2025-05-14"
			if fastMode {
				betas += "," + fastModeBeta
			}
			r.Header.Set("anthropic-beta", betas)
			r.Header.Set("User-Agent", "claude-cli/"+claudeCodeVersion)
			r.Header.Set("x-app", "cli")
		} else {
			r.Header.Set("X-API-Key", apiKey)
			if fastMode {
				r.Header.Set("anthropic-beta", fastModeBeta)
			}
		}
		return r, nil
	}

	policy := retry.DefaultPolicy
	if fastMode {
		// A fast-mode request rejected for want of usage credits will be
		// rejected again a second later: retrying it five times with backoff
		// only delays the fallback to standard speed by half a minute.
		policy.Retryable = func(resp *http.Response, body []byte) bool {
			return resp.StatusCode != http.StatusTooManyRequests || !isFastModeUnavailable(body)
		}
	}

	resp, err := retry.Do(ctx, a.client, buildReq, policy, nil)
	if err != nil {
		return nil, fmt.Errorf("anthropic: %w", err)
	}

	// Non-retryable error status (400, 401, etc.) — returned as-is by retry.Do.
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close() //nolint:errcheck
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		// Fast mode is a paid upgrade, and an account without usage credits is
		// refused it while its ordinary quota still works. Falling back keeps
		// the turn alive at standard speed instead of failing the request the
		// user actually asked for; the caller turns the setting off so the
		// next turn doesn't pay this round trip again.
		if fastMode && resp.StatusCode == http.StatusTooManyRequests && isFastModeUnavailable(errBody) {
			slowReq := req
			slowReq.Options.Fast = false
			if req.Options.OnFastUnavailable != nil {
				req.Options.OnFastUnavailable()
			}
			return a.Stream(ctx, slowReq)
		}
		return nil, fmt.Errorf("anthropic: HTTP %d: %s", resp.StatusCode, string(errBody))
	}

	// Rate-limit state comes back in the response headers (available now, before
	// the SSE body). Emit it up front as its own event so callers get instant,
	// per-request plan-usage / overage awareness — and so it survives even if the
	// stream later errors or is cancelled mid-flight.
	rl := parseRateLimit(resp.Header)

	ch := make(chan core.AssistantEvent, 64)

	// Only the tool list is needed to decode the stream. Capturing req itself
	// would keep the whole request — every materialized image in base64 —
	// reachable for as long as the SSE body is being consumed.
	tools := req.Tools

	go func() {
		defer resp.Body.Close() //nolint:errcheck
		defer close(ch)
		if rl != nil {
			ch <- core.AssistantEvent{Type: core.ProviderEventRateLimit, RateLimit: rl}
		}
		body := io.Reader(sseutil.NewIdleTimeoutReader(resp.Body, 5*time.Minute))
		a.consumeStream(ctx, body, ch, tools, oauthMode)
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

	err := parseSSEFramesUntil(body, func(eventType, data string) bool {
		// Check context cancellation
		if ctx.Err() != nil {
			return true
		}

		event := a.mapEvent(eventType, data, state)
		if event == nil {
			return false
		}

		ch <- *event

		if event.IsTerminal() {
			sentTerminal = true
			return true
		}
		return false
	})

	if sentTerminal {
		return
	}
	if ctx.Err() != nil {
		ch <- core.AssistantEvent{
			Type:  core.ProviderEventError,
			Error: ctx.Err(),
		}
		sentTerminal = true
		return
	}
	if err != nil {
		ch <- core.AssistantEvent{
			Type:  core.ProviderEventError,
			Error: fmt.Errorf("SSE parse: %w", err),
		}
		sentTerminal = true
	}
}

// streamState tracks the evolving message across SSE events.
type streamState struct {
	message        core.Message
	contentIdx     int
	blockType      string // current block type being built
	jsonAccum      string // accumulated JSON for tool_use input
	toolCallID     string
	toolCallName   string
	requestTools   []core.ToolSpec // original tool specs for reverse name mapping
	isOAuth        bool            // whether this request used OAuth (for tool name mapping)
	textAccum      strings.Builder
	thinkingAccum  strings.Builder
	signatureAccum strings.Builder

	// Partial JSON parsing for streaming tool call arguments.
	partialParser jsonutil.PartialParser
	lastParseLen  int // len(jsonAccum) at last partial parse
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
				InputTokens  int    `json:"input_tokens"`
				OutputTokens int    `json:"output_tokens"`
				CacheRead    int    `json:"cache_read_input_tokens"`
				CacheCreate  int    `json:"cache_creation_input_tokens"`
				Speed        string `json:"speed"`
				// cache_creation splits the write between the 5-minute and
				// 1-hour windows, which are billed at 1.25x and 2x input.
				// Absent on older responses; then the whole write is 5m.
				CacheCreation *struct {
					Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
					Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
				} `json:"cache_creation"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return &core.AssistantEvent{
			Type:  core.ProviderEventError,
			Error: fmt.Errorf("parse message_start: %w (data: %.100s)", err, data),
		}
	}

	usage := &core.Usage{
		Input:      payload.Message.Usage.InputTokens,
		Output:     payload.Message.Usage.OutputTokens,
		CacheRead:  payload.Message.Usage.CacheRead,
		CacheWrite: payload.Message.Usage.CacheCreate,
		Fast:       payload.Message.Usage.Speed == "fast",
	}
	if cc := payload.Message.Usage.CacheCreation; cc != nil {
		usage.CacheWrite1h = cc.Ephemeral1h
		// cache_creation_input_tokens is documented as the total, but trust the
		// breakdown when it disagrees: the split is what gets billed.
		if total := cc.Ephemeral5m + cc.Ephemeral1h; total > usage.CacheWrite {
			usage.CacheWrite = total
		}
	}

	state.message = core.Message{
		Role:      "assistant",
		Provider:  "anthropic",
		Model:     payload.Message.Model,
		Usage:     usage,
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
			Data string `json:"data,omitempty"` // redacted_thinking payload
		} `json:"content_block"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return &core.AssistantEvent{
			Type:  core.ProviderEventError,
			Error: fmt.Errorf("parse content_block_start: %w (data: %.100s)", err, data),
		}
	}

	state.materializeContentBlock()
	state.textAccum.Reset()
	state.thinkingAccum.Reset()
	state.signatureAccum.Reset()
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
		state.partialParser.Reset()
		state.lastParseLen = 0
		state.message.Content = append(state.message.Content, core.ToolCallContent(
			payload.ContentBlock.ID,
			toolName,
			nil,
		))
		return &core.AssistantEvent{
			Type:         core.ProviderEventToolCallStart,
			ContentIndex: payload.Index,
			ToolCallID:   payload.ContentBlock.ID,
			ToolName:     toolName,
		}

	case "redacted_thinking":
		// Preserve the encrypted block verbatim: Anthropic requires it be
		// sent back in later turns that include tool use. Appending keeps our
		// Content aligned with the API's block indices. No user-visible event.
		state.message.Content = append(state.message.Content, core.Content{
			Type:              "thinking",
			Redacted:          true,
			ThinkingSignature: payload.ContentBlock.Data,
		})
		return nil

	default:
		// Unknown block type: append an (empty) placeholder so subsequent
		// deltas, which reference the API's block index, still line up with
		// our Content slice. An empty thinking block is dropped on rebuild.
		state.message.Content = append(state.message.Content, core.Content{Type: "thinking"})
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
			state.textAccum.WriteString(payload.Delta.Text)
		}
		return &core.AssistantEvent{
			Type:         core.ProviderEventTextDelta,
			ContentIndex: idx,
			Delta:        payload.Delta.Text,
		}

	case "thinking_delta":
		if idx < len(state.message.Content) {
			state.thinkingAccum.WriteString(payload.Delta.Thinking)
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
			state.signatureAccum.WriteString(payload.Delta.Signature)
		}
		return nil // No user-visible event for signatures

	case "input_json_delta":
		state.jsonAccum += payload.Delta.PartialJSON
		evt := &core.AssistantEvent{
			Type:         core.ProviderEventToolCallDelta,
			ContentIndex: idx,
			Delta:        payload.Delta.PartialJSON,
			ToolCallID:   state.toolCallID,
			ToolName:     state.toolCallName,
		}
		// Throttled partial parse: only every 200 bytes to cap CPU cost.
		if len(state.jsonAccum)-state.lastParseLen >= 200 {
			if parsed := state.partialParser.Parse(state.jsonAccum); parsed != nil {
				evt.PartialArgs = parsed
				if idx < len(state.message.Content) {
					state.message.Content[idx].Arguments = parsed
				}
			}
			state.lastParseLen = len(state.jsonAccum)
		}
		return evt

	default:
		return nil
	}
}

func (a *Anthropic) handleContentBlockStop(state *streamState) *core.AssistantEvent {
	idx := state.contentIdx
	state.materializeContentBlock()

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
		// Parse accumulated JSON into arguments (authoritative final parse).
		if idx < len(state.message.Content) && state.jsonAccum != "" {
			var args map[string]any
			if err := json.Unmarshal([]byte(state.jsonAccum), &args); err == nil {
				state.message.Content[idx].Arguments = args
			} else {
				// The authoritative JSON didn't parse — typically truncated by
				// max_tokens mid-stream. The incremental partial parser may have
				// left a TRUNCATED Arguments map (e.g. a half-written `content`
				// string for a write); discard it so the tool runs on corrupt
				// input is impossible. With nil args ValidateToolCall rejects the
				// call and the model gets a clean error to retry, instead of a
				// silently corrupted file.
				state.message.Content[idx].Arguments = nil
			}
		}
		state.jsonAccum = ""
		return &core.AssistantEvent{
			Type:         core.ProviderEventToolCallEnd,
			ContentIndex: idx,
			ToolCallID:   state.toolCallID,
			ToolName:     state.toolCallName,
		}

	default:
		return nil
	}
}

func (a *Anthropic) handleMessageDelta(data string, state *streamState) *core.AssistantEvent {
	var payload struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
			// stop_details carries the human-readable reason on a refusal (and
			// potentially other safety stops). category is "cyber"/"bio"/null;
			// explanation may be null. See Anthropic Messages API RefusalStopDetails.
			StopDetails *struct {
				Explanation string `json:"explanation"`
			} `json:"stop_details"`
		} `json:"delta"`
		Usage struct {
			OutputTokens int     `json:"output_tokens"`
			Speed        *string `json:"speed"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return &core.AssistantEvent{
			Type:  core.ProviderEventError,
			Error: fmt.Errorf("parse message_delta: %w (data: %.100s)", err, data),
		}
	}

	// Guard against a stray message_delta with no stop_reason wiping a value we
	// may already have captured.
	if payload.Delta.StopReason != "" {
		state.message.StopReason = payload.Delta.StopReason
	}
	// Preserve the refusal explanation for the loop to surface as a visible
	// error. The turn's partial content (already streamed) stays intact.
	if payload.Delta.StopDetails != nil && payload.Delta.StopDetails.Explanation != "" {
		state.message.ErrorMessage = payload.Delta.StopDetails.Explanation
	}
	if state.message.Usage != nil {
		state.message.Usage.Output = payload.Usage.OutputTokens
		if payload.Usage.Speed != nil {
			state.message.Usage.Fast = *payload.Usage.Speed == "fast"
		}
		state.message.Usage.TotalTokens = state.message.Usage.Input +
			state.message.Usage.Output +
			state.message.Usage.CacheRead +
			state.message.Usage.CacheWrite
	}

	return nil // No normalized event for message_delta; info captured in state
}

func (a *Anthropic) handleMessageStop(state *streamState) *core.AssistantEvent {
	state.materializeContentBlock()
	final := state.message // copy
	return &core.AssistantEvent{
		Type:    core.ProviderEventDone,
		Message: &final,
	}
}

func (state *streamState) materializeContentBlock() {
	if state.contentIdx >= len(state.message.Content) {
		return
	}
	switch state.blockType {
	case "text":
		state.message.Content[state.contentIdx].Text = state.textAccum.String()
	case "thinking":
		state.message.Content[state.contentIdx].Thinking = state.thinkingAccum.String()
		state.message.Content[state.contentIdx].ThinkingSignature = state.signatureAccum.String()
	}
}

// isFastModeUnavailable reports whether a 429 rejected fast mode itself rather
// than throttling the account. The API answers a fast-mode request from an
// account without usage credits with "Usage credits are required for fast
// mode." — a verdict that will not change on a retry, unlike an ordinary rate
// limit.
func isFastModeUnavailable(body []byte) bool {
	return bytes.Contains(bytes.ToLower(body), []byte("fast mode"))
}
