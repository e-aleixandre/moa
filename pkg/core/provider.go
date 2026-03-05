package core

import (
	"context"
	"encoding/json"
)

// Provider streams LLM responses. Each provider (Anthropic, OpenAI, etc.)
// implements this interface, emitting normalized AssistantEvents.
//
// Error contract:
//   - Returns error immediately for pre-stream failures (auth, invalid model, network).
//   - If channel is returned, it ALWAYS receives exactly one terminal event
//     ("done" or "error") before being closed.
//   - The caller must drain the channel to avoid goroutine leaks.
//   - Context cancellation causes an "error" event with ctx.Err().
type Provider interface {
	Stream(ctx context.Context, req Request) (<-chan AssistantEvent, error)
}

// Request contains everything needed for an LLM call.
type Request struct {
	Model   Model
	System  string     // System prompt
	Messages []Message // Conversation history (user, assistant, tool_result)
	Tools   []ToolSpec // Available tools for tool_use
	Options StreamOptions
}

// ToolSpec is a tool definition sent to the LLM (name + description + JSON schema).
// Separate from the executable Tool to keep the provider layer dependency-free.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Model identifies an LLM model.
type Model struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	API       string `json:"api"`
	Name      string `json:"name"`
	MaxInput  int    `json:"max_input"`
	MaxOutput int    `json:"max_output"`
}

// StreamOptions configures an LLM request.
type StreamOptions struct {
	Temperature    *float64 `json:"temperature,omitempty"`
	MaxTokens      *int     `json:"max_tokens,omitempty"`
	APIKey         string   `json:"-"`
	ThinkingLevel  string   `json:"thinking_level,omitempty"`
	CacheRetention string   `json:"cache_retention,omitempty"`
}

// AssistantEvent is emitted by providers during streaming.
//
// Terminal events: "done" (success) or "error" (failure).
// Every stream ends with exactly one terminal event, then channel close.
type AssistantEvent struct {
	Type         string   `json:"type"`
	ContentIndex int      `json:"content_index,omitempty"`
	Delta        string   `json:"delta,omitempty"`
	Partial      *Message `json:"partial,omitempty"`
	Message      *Message `json:"message,omitempty"`
	Error        error    `json:"-"`
}

// IsTerminal returns true for "done" or "error" events.
func (e AssistantEvent) IsTerminal() bool {
	return e.Type == ProviderEventDone || e.Type == ProviderEventError
}

// Provider event type constants.
const (
	ProviderEventStart         = "start"
	ProviderEventTextStart     = "text_start"
	ProviderEventTextDelta     = "text_delta"
	ProviderEventTextEnd       = "text_end"
	ProviderEventThinkingStart = "thinking_start"
	ProviderEventThinkingDelta = "thinking_delta"
	ProviderEventThinkingEnd   = "thinking_end"
	ProviderEventToolCallStart = "toolcall_start"
	ProviderEventToolCallDelta = "toolcall_delta"
	ProviderEventToolCallEnd   = "toolcall_end"
	ProviderEventDone          = "done"
	ProviderEventError         = "error"
)
