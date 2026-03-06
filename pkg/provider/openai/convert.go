package openai

import (
	"encoding/json"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// openaiRequest is the JSON body for POST /v1/chat/completions.
type openaiRequest struct {
	Model          string           `json:"model"`
	Messages       []map[string]any `json:"messages"`
	Tools          []map[string]any `json:"tools,omitempty"`
	Stream         bool             `json:"stream"`
	StreamOptions  *streamOptions   `json:"stream_options,omitempty"`
	MaxTokens      *int             `json:"max_completion_tokens,omitempty"`
	Temperature    *float64         `json:"temperature,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

func buildRequestBody(req core.Request) ([]byte, error) {
	r := openaiRequest{
		Model:  req.Model.ID,
		Stream: true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}

	r.Messages = convertMessages(req.System, req.Messages)

	if len(req.Tools) > 0 {
		r.Tools = convertToolSpecs(req.Tools)
	}

	if req.Options.MaxTokens != nil {
		r.MaxTokens = req.Options.MaxTokens
	}
	if req.Options.Temperature != nil {
		r.Temperature = req.Options.Temperature
	}

	// Map thinking level to OpenAI reasoning_effort.
	r.ReasoningEffort = mapReasoningEffort(req.Options.ThinkingLevel)

	return json.Marshal(r)
}

// mapReasoningEffort maps our thinking levels to OpenAI reasoning_effort.
// Only applicable to o-series and codex models; others ignore it.
func mapReasoningEffort(level string) string {
	switch level {
	case "off", "":
		return ""
	case "minimal", "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	default:
		return "medium"
	}
}

// convertMessages maps core messages to OpenAI Chat Completions format.
func convertMessages(system string, msgs []core.Message) []map[string]any {
	var result []map[string]any

	// System prompt as developer message (recommended for newer models).
	if system != "" {
		result = append(result, map[string]any{
			"role":    "developer",
			"content": system,
		})
	}

	for _, msg := range msgs {
		apiMsg := convertMessage(msg)
		if apiMsg != nil {
			result = append(result, apiMsg)
		}
	}

	return result
}

func convertMessage(msg core.Message) map[string]any {
	switch msg.Role {
	case "user":
		return map[string]any{
			"role":    "user",
			"content": convertUserContent(msg.Content),
		}

	case "assistant":
		m := map[string]any{
			"role": "assistant",
		}
		// Extract text and tool calls separately.
		text := extractTextParts(msg.Content)
		toolCalls := extractToolCalls(msg.Content)

		if text != "" {
			m["content"] = text
		}
		if len(toolCalls) > 0 {
			m["tool_calls"] = toolCalls
		}
		return m

	case "tool_result":
		// OpenAI uses role:"tool" with tool_call_id.
		text := extractTextParts(msg.Content)
		return map[string]any{
			"role":         "tool",
			"tool_call_id": msg.ToolCallID,
			"content":      text,
		}

	default:
		return nil
	}
}

// convertUserContent handles text and image content blocks.
func convertUserContent(blocks []core.Content) any {
	// If only text, send as string for simplicity.
	texts := make([]string, 0)
	hasNonText := false
	for _, b := range blocks {
		if b.Type == "text" {
			texts = append(texts, b.Text)
		} else {
			hasNonText = true
		}
	}
	if !hasNonText {
		return strings.Join(texts, "\n")
	}

	// Mixed content → array.
	var parts []map[string]any
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, map[string]any{
				"type": "text",
				"text": b.Text,
			})
		case "image":
			parts = append(parts, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": "data:" + b.MimeType + ";base64," + b.Data,
				},
			})
		}
	}
	return parts
}

func extractTextParts(blocks []core.Content) string {
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "")
}

func extractToolCalls(blocks []core.Content) []map[string]any {
	var calls []map[string]any
	for _, b := range blocks {
		if b.Type != "tool_call" {
			continue
		}
		argsJSON, _ := json.Marshal(b.Arguments)
		calls = append(calls, map[string]any{
			"id":   b.ToolCallID,
			"type": "function",
			"function": map[string]any{
				"name":      b.ToolName,
				"arguments": string(argsJSON),
			},
		})
	}
	return calls
}

func convertToolSpecs(specs []core.ToolSpec) []map[string]any {
	result := make([]map[string]any, 0, len(specs))
	for _, s := range specs {
		fn := map[string]any{
			"name":        s.Name,
			"description": s.Description,
		}
		if len(s.Parameters) > 0 {
			var schema any
			if err := json.Unmarshal(s.Parameters, &schema); err == nil {
				fn["parameters"] = schema
			}
		}
		result = append(result, map[string]any{
			"type":     "function",
			"function": fn,
		})
	}
	return result
}
