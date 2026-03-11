package core

import (
	"encoding/json"
	"time"
)

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

// NewUserMessageWithContent creates a user message with arbitrary content blocks.
func NewUserMessageWithContent(content []Content) Message {
	return Message{
		Role:      "user",
		Content:   content,
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

// EstimateTokens estimates the token count of a single message using
// a chars/4 heuristic. Conservative (overestimates slightly).
func EstimateTokens(m Message) int {
	chars := 0
	for _, c := range m.Content {
		switch c.Type {
		case "text":
			chars += len(c.Text)
		case "thinking":
			chars += len(c.Thinking)
		case "tool_call":
			chars += len(c.ToolName)
			if c.Arguments != nil {
				b, _ := json.Marshal(c.Arguments)
				chars += len(b)
			}
		case "image":
			chars += 4800 // ~1200 tokens
		}
	}
	if m.Role == "tool_result" {
		chars += len(m.ToolName) + len(m.ToolCallID)
	}
	if chars == 0 {
		return 0
	}
	return (chars + 3) / 4
}

// ContextEstimate holds the result of a context size estimation.
type ContextEstimate struct {
	Tokens         int // total estimated context tokens
	UsageTokens    int // from provider-reported usage (0 if none valid)
	TrailingTokens int // estimated tokens for messages after last valid usage
	OverheadTokens int // system prompt + tool specs
}

// EstimateContextTokens estimates total context size including system prompt
// and tool spec overhead. Uses provider-reported Usage from the last assistant
// message whose compaction epoch matches the current one. Stale usage from
// pre-compaction messages is ignored.
func EstimateContextTokens(msgs []AgentMessage, systemPrompt string, toolSpecs []ToolSpec, compactionEpoch int) ContextEstimate {
	overhead := (len(systemPrompt) + 3) / 4
	for _, t := range toolSpecs {
		overhead += (len(t.Name) + len(t.Description) + len(t.Parameters) + 3) / 4
	}

	// Find last valid assistant usage (matching current compaction epoch).
	lastUsageIdx := -1
	var lastUsage *Usage
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "assistant" && m.Usage != nil && m.StopReason != "error" {
			msgEpoch := 0
			if m.Custom != nil {
				if e, ok := m.Custom["compaction_epoch"].(float64); ok {
					msgEpoch = int(e)
				}
				// Also handle int (non-JSON path).
				if e, ok := m.Custom["compaction_epoch"].(int); ok {
					msgEpoch = e
				}
			}
			if msgEpoch == compactionEpoch {
				lastUsageIdx = i
				lastUsage = m.Usage
				break
			}
		}
	}

	if lastUsage == nil {
		// No valid provider usage — estimate everything from chars.
		total := 0
		for _, m := range msgs {
			total += EstimateTokens(m.Message)
		}
		return ContextEstimate{
			Tokens:         total + overhead,
			TrailingTokens: total,
			OverheadTokens: overhead,
		}
	}

	// Provider usage already includes system prompt + tool specs + all messages
	// up to the response, so we don't add overhead again.
	usageTokens := lastUsage.TotalTokens
	if usageTokens == 0 {
		usageTokens = lastUsage.Input + lastUsage.Output + lastUsage.CacheRead + lastUsage.CacheWrite
	}

	trailing := 0
	for i := lastUsageIdx + 1; i < len(msgs); i++ {
		trailing += EstimateTokens(msgs[i].Message)
	}

	return ContextEstimate{
		Tokens:         usageTokens + trailing,
		UsageTokens:    usageTokens,
		TrailingTokens: trailing,
		OverheadTokens: overhead,
	}
}
