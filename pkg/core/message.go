package core

import "time"

// Content is a tagged union. Type determines which fields are populated.
//
//	"text"      → Text
//	"thinking"  → Thinking, ThinkingSignature, Redacted
//	"image"     → Data, MimeType
//	"tool_call" → ToolCallID, ToolName, Arguments
type Content struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// thinking
	Thinking          string `json:"thinking,omitempty"`
	ThinkingSignature string `json:"thinking_signature,omitempty"`
	Redacted          bool   `json:"redacted,omitempty"`

	// image
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mime_type,omitempty"`

	// tool_call
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	Arguments  map[string]any `json:"arguments,omitempty"`
}

// Constructors for clarity.
func TextContent(text string) Content       { return Content{Type: "text", Text: text} }
func ImageContent(data, mime string) Content { return Content{Type: "image", Data: data, MimeType: mime} }
func ThinkingContent(text string) Content    { return Content{Type: "thinking", Thinking: text} }
func ToolCallContent(id, name string, args map[string]any) Content {
	return Content{Type: "tool_call", ToolCallID: id, ToolName: name, Arguments: args}
}

// Message is a tagged union. Role determines which fields are relevant.
//
//	"user"        → Content
//	"assistant"   → Content, Provider, Model, Usage, StopReason
//	"tool_result" → ToolCallID, ToolName, Content, IsError
type Message struct {
	Role      string    `json:"role"`
	Content   []Content `json:"content"`
	Timestamp int64     `json:"timestamp"`

	// assistant-only
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
	Usage        *Usage `json:"usage,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`

	// tool_result-only
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
}

// NewUserMessage creates a user message with text content.
func NewUserMessage(text string) Message {
	return Message{
		Role:      "user",
		Content:   []Content{TextContent(text)},
		Timestamp: time.Now().Unix(),
	}
}

// NewToolResultMessage creates a tool_result message.
func NewToolResultMessage(toolCallID, toolName string, content []Content, isError bool) Message {
	return Message{
		Role:       "tool_result",
		Content:    content,
		Timestamp:  time.Now().Unix(),
		ToolCallID: toolCallID,
		ToolName:   toolName,
		IsError:    isError,
	}
}

// AgentMessage wraps Message with extension-custom data.
// Custom messages (role not user/assistant/tool_result) are filtered before LLM calls.
type AgentMessage struct {
	Message
	Custom map[string]any `json:"custom,omitempty"`
}

// IsLLMMessage returns true if this message should be sent to the LLM.
func (m AgentMessage) IsLLMMessage() bool {
	return m.Role == "user" || m.Role == "assistant" || m.Role == "tool_result"
}

// WrapMessage converts a Message to an AgentMessage.
func WrapMessage(m Message) AgentMessage {
	return AgentMessage{Message: m}
}

// Usage tracks token consumption for a single LLM call.
type Usage struct {
	Input       int `json:"input"`
	Output      int `json:"output"`
	CacheRead   int `json:"cache_read"`
	CacheWrite  int `json:"cache_write"`
	TotalTokens int `json:"total_tokens"`
}
