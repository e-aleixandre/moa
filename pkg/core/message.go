package core

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// Content is a tagged union. Type determines which fields are populated.
//
//	"text"      → Text
//	"thinking"  → Thinking, ThinkingSignature, Redacted
//	"image"     → Data, MimeType
//	"document"  → Data, MimeType, Filename
//	"tool_call" → ToolCallID, ToolName, Arguments
type Content struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`
	// TextSignature carries provider round-trip metadata for a text/message
	// block so the exact item can be replayed on the next request. For the
	// OpenAI Responses API this is a small JSON blob {id, phase} — the model's
	// output message id and its phase ("commentary"/"final_answer"). OpenAI
	// warns that dropping the phase when replaying manually causes "early
	// stopping and other misbehavior", which manifests as empty/stalled turns.
	// Opaque to everything except the provider that produced it.
	TextSignature string `json:"text_signature,omitempty"`

	// thinking
	Thinking          string `json:"thinking,omitempty"`
	ThinkingSignature string `json:"thinking_signature,omitempty"`
	Redacted          bool   `json:"redacted,omitempty"`

	// image
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Filename string `json:"filename,omitempty"`

	// tool_call
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	Arguments  map[string]any `json:"arguments,omitempty"`
	// ToolCallItemID is the provider's output-item id for a tool_call (OpenAI
	// Responses: the "fc_..." id, distinct from ToolCallID which is the
	// "call_id" that pairs the call with its function_call_output). Preserved
	// so the function_call item can be replayed with its original id, matching
	// how the reasoning item that preceded it was paired. Empty for providers
	// that don't use a separate item id.
	ToolCallItemID string `json:"tool_call_item_id,omitempty"`
}

// Constructors for clarity.
func TextContent(text string) Content { return Content{Type: "text", Text: text} }
func ImageContent(data, mime string) Content {
	return Content{Type: "image", Data: data, MimeType: mime}
}
func DocumentContent(data, mime, filename string) Content {
	return Content{Type: "document", Data: data, MimeType: mime, Filename: filename}
}
func ThinkingContent(text string) Content { return Content{Type: "thinking", Thinking: text} }
func ToolCallContent(id, name string, args map[string]any) Content {
	return Content{Type: "tool_call", ToolCallID: id, ToolName: name, Arguments: args}
}

// NativeDocBytes sums the decoded size of the native document/image blocks in
// content — the base64 payloads that count against a session's native-content
// budget. Text/thinking/tool blocks contribute nothing.
func NativeDocBytes(content []Content) int64 {
	var total int64
	for _, c := range content {
		if c.Type == "image" || c.Type == "document" {
			total += int64(base64.StdEncoding.DecodedLen(len(c.Data)))
		}
	}
	return total
}

// Clone returns a deep copy of the content: every field is copied by value
// except Arguments (a map[string]any), which is cloned so the copy shares no
// mutable backing state with the original. Nested values inside Arguments are
// copied via cloneAny (maps and slices are rebuilt recursively). Used at
// ownership boundaries (e.g. when the agent takes a caller-supplied content
// block into its own state) so a later mutation by the caller can't change the
// stored message or race a concurrent reader.
func (c Content) Clone() Content {
	c.Arguments = cloneArgs(c.Arguments)
	return c
}

// CloneContent returns a deep copy of a content slice (see Content.Clone). A nil
// input yields a nil output.
func CloneContent(in []Content) []Content {
	if in == nil {
		return nil
	}
	out := make([]Content, len(in))
	for i, c := range in {
		out[i] = c.Clone()
	}
	return out
}

func cloneArgs(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneAny(v)
	}
	return out
}

// cloneAny deep-copies the JSON-shaped values that can appear in a tool_call's
// Arguments (maps, slices, scalars). Scalars are returned as-is.
func cloneAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneArgs(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cloneAny(e)
		}
		return out
	default:
		return t
	}
}

// Message is a tagged union. Role determines which fields are relevant.
//
//	"user"        → Content
//	"assistant"   → Content, Provider, Model, Usage, StopReason
//	"tool_result" → ToolCallID, ToolName, Content, IsError
type Message struct {
	MsgID     string    `json:"msg_id,omitempty"`
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

// EnsureMsgID assigns a stable identifier when the message does not have one.
func (m *Message) EnsureMsgID() {
	if m.MsgID != "" {
		return
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		m.MsgID = hex.EncodeToString(b)
		return
	}
	m.MsgID = time.Now().UTC().Format("20060102150405.000000000")
}

// SteerItem is a queued item in the agent's unified queue rail. It is either a
// steering message (text, optionally with image/content blocks) injected into a
// run, or a queued command that acts as a turn barrier (see Command). Items are
// consumed in strict FIFO order, so a command queued between two messages runs
// exactly in that position.
type SteerItem struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	// Content, when non-nil, is the full payload of a steer (text plus image or
	// other content blocks). It is injected with NewUserMessageWithContent. A
	// nil Content means a plain-text steer carried in Text.
	Content []Content `json:"content,omitempty"`
	// Command, when non-empty, marks this item as a queued command (a BARRIER):
	// it holds the raw normalized command line (e.g. "/compact", "/model sonnet").
	// A barrier item is never injected as a conversation message — it stops the
	// queue drain, and is executed at the next idle point (RunEnded) by the bus.
	// Invariant: a barrier carries no Content, and an Internal item is never a
	// barrier.
	Command string `json:"command,omitempty"`
	// Internal marks a system-generated steer (e.g. a subagent/bash completion
	// injected into the parent run) as opposed to a user-typed message. Internal
	// steers are delivered to the agent but excluded from the authoritative
	// queue snapshot, since their delivery event is suppressed and they must not
	// surface as user-visible "queued" chips.
	Internal bool `json:"-"`
}

// IsBarrier reports whether this item is a queued command that stops the run
// (a turn barrier) rather than a steer injected into the current run.
func (it SteerItem) IsBarrier() bool { return it.Command != "" }

// NewSteerID mints a random identifier for a steer item, using the same
// crypto/rand mechanism as Message.EnsureMsgID.
func NewSteerID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		return hex.EncodeToString(b)
	}
	return time.Now().UTC().Format("20060102150405.000000000")
}

// NewMsgID mints a stable message identifier, using the same mechanism as
// Message.EnsureMsgID. Used when a caller needs a message's ID before the
// message is built (e.g. to correlate a later event with it).
func NewMsgID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err == nil {
		return hex.EncodeToString(b)
	}
	return time.Now().UTC().Format("20060102150405.000000000")
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

// RateLimit captures the unified rate-limit state a provider reports on each
// response (Anthropic's anthropic-ratelimit-unified-* headers).
//
// Utilization fields are fractions in [0,1], or -1 when the corresponding header
// was absent/invalid — callers must treat -1 as "unknown" and NOT overwrite a
// known value with it (the endpoint is reverse-engineered and may change shape).
//
// It lets callers see, per request, how much of each plan window is used and
// whether the request was served from pay-as-you-go "extra usage" — instantly,
// without polling the account-global usage endpoint.
type RateLimit struct {
	Status              string  `json:"status,omitempty"`               // allowed / allowed_warning / rejected
	RepresentativeClaim string  `json:"representative_claim,omitempty"` // window that currently binds: five_hour / seven_day / overage / ...
	FiveHourUtil        float64 `json:"five_hour_util"`                 // [0,1], or -1 if unknown
	SevenDayUtil        float64 `json:"seven_day_util"`                 // [0,1], or -1 if unknown
	OverageStatus       string  `json:"overage_status,omitempty"`
	OverageUtil         float64 `json:"overage_util"` // [0,1], or -1 if unknown
}

// OnOverage reports whether the request is currently being served from extra
// usage — i.e. the binding rate-limit window is the overage bucket.
func (r RateLimit) OnOverage() bool {
	return r.RepresentativeClaim == "overage"
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
		case "document":
			chars += 12000 // ~3000 tokens; PDFs vary, overestimate
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

// ExtractFinalAssistantText returns the concatenated text content of the last
// assistant message in the conversation. Returns "" if none found.
func ExtractFinalAssistantText(msgs []AgentMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		var parts []string
		for _, c := range msgs[i].Content {
			if c.Type == "text" && c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}
