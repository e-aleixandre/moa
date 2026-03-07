package openai

import (
	"encoding/json"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// responsesRequest is the JSON body for POST /v1/responses (or /codex/responses).
type responsesRequest struct {
	Model        string           `json:"model"`
	Input        []map[string]any `json:"input"`
	Instructions string           `json:"instructions,omitempty"`
	Tools        []map[string]any `json:"tools,omitempty"`
	Stream       bool             `json:"stream"`
	Store        bool             `json:"store"`
	MaxTokens    *int             `json:"max_output_tokens,omitempty"`
	Temperature  *float64         `json:"temperature,omitempty"`
	Reasoning    *reasoning       `json:"reasoning,omitempty"`
	ToolChoice   string           `json:"tool_choice,omitempty"`
	Include      []string         `json:"include,omitempty"`
}

type reasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

func buildRequestBody(req core.Request) ([]byte, error) {
	r := responsesRequest{
		Model:        req.Model.ID,
		Stream:       true,
		Store:        false,
		Instructions: req.System,
		ToolChoice:   "auto",
		Include:      []string{"reasoning.encrypted_content"},
	}

	r.Input = convertMessages(req.Messages)

	if len(req.Tools) > 0 {
		r.Tools = convertToolSpecs(req.Tools)
	}

	if req.Options.MaxTokens != nil {
		r.MaxTokens = req.Options.MaxTokens
	}
	if req.Options.Temperature != nil {
		r.Temperature = req.Options.Temperature
	}

	if effort := mapReasoningEffort(req.Options.ThinkingLevel); effort != "" {
		r.Reasoning = &reasoning{Effort: effort, Summary: "auto"}
	}

	return json.Marshal(r)
}

// mapReasoningEffort maps our thinking levels to OpenAI reasoning effort.
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

// convertMessages maps core messages to Responses API input format.
func convertMessages(msgs []core.Message) []map[string]any {
	var result []map[string]any

	for _, msg := range msgs {
		items := convertMessage(msg)
		result = append(result, items...)
	}

	return result
}

func convertMessage(msg core.Message) []map[string]any {
	switch msg.Role {
	case "user":
		return []map[string]any{
			{
				"role":    "user",
				"content": convertUserContent(msg.Content),
			},
		}

	case "assistant":
		return convertAssistantMessage(msg)

	case "tool_result":
		text := extractTextParts(msg.Content)
		return []map[string]any{
			{
				"type":    "function_call_output",
				"call_id": msg.ToolCallID,
				"output":  text,
			},
		}

	default:
		return nil
	}
}

// convertAssistantMessage converts an assistant message to Responses API items.
// In the Responses API, assistant content is represented as individual output items
// (message, function_call, reasoning) at the top level of the input array.
func convertAssistantMessage(msg core.Message) []map[string]any {
	var items []map[string]any

	for _, c := range msg.Content {
		switch c.Type {
		case "text":
			items = append(items, map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": c.Text, "annotations": []any{}},
				},
				"status": "completed",
			})

		case "tool_call":
			args := c.Arguments
			if args == nil {
				args = map[string]any{}
			}
			argsJSON, _ := json.Marshal(args)
			items = append(items, map[string]any{
				"type":      "function_call",
				"call_id":   c.ToolCallID,
				"name":      c.ToolName,
				"arguments": string(argsJSON),
			})

		case "thinking":
			// Re-serialize the encrypted reasoning item if we have a signature.
			if c.ThinkingSignature != "" {
				var item map[string]any
				if json.Unmarshal([]byte(c.ThinkingSignature), &item) == nil {
					items = append(items, item)
				}
			}
		}
	}

	return items
}

// convertUserContent handles text and image content blocks.
func convertUserContent(blocks []core.Content) []map[string]any {
	var parts []map[string]any
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, map[string]any{
				"type": "input_text",
				"text": b.Text,
			})
		case "image":
			parts = append(parts, map[string]any{
				"type":      "input_image",
				"detail":    "auto",
				"image_url": "data:" + b.MimeType + ";base64," + b.Data,
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

func convertToolSpecs(specs []core.ToolSpec) []map[string]any {
	result := make([]map[string]any, 0, len(specs))
	for _, s := range specs {
		tool := map[string]any{
			"type":        "function",
			"name":        s.Name,
			"description": s.Description,
		}
		if len(s.Parameters) > 0 {
			var schema any
			if err := json.Unmarshal(s.Parameters, &schema); err == nil {
				tool["parameters"] = schema
			}
		}
		result = append(result, tool)
	}
	return result
}
