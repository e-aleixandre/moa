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

func buildRequestBody(req core.Request, supportsDocuments bool) ([]byte, error) {
	r := responsesRequest{
		Model:        req.Model.ID,
		Stream:       true,
		Store:        false,
		Instructions: req.System,
		ToolChoice:   "auto",
		Include:      []string{"reasoning.encrypted_content"},
	}

	r.Input = convertMessages(req.Messages, supportsDocuments, req.Model.ID)

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
// OpenAI supports: none, minimal, low, medium, high, xhigh.
func mapReasoningEffort(level string) string {
	switch strings.ToLower(level) {
	case "off", "none", "":
		return ""
	case "minimal":
		return "minimal"
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh":
		return "xhigh"
	default:
		return "medium"
	}
}

// convertMessages maps core messages to Responses API input format.
// supportsDocuments gates native "document" blocks: when false (e.g. the codex
// OAuth path), any persisted document block is degraded to a text note instead
// of being emitted as an input_file the provider would reject or silently drop.
// modelID is the target model of THIS request; assistant items produced by a
// different model omit their provider-assigned output-item ids to avoid pairing
// validation errors (see convertAssistantMessage).
func convertMessages(msgs []core.Message, supportsDocuments bool, modelID string) []map[string]any {
	var result []map[string]any

	for _, msg := range msgs {
		items := convertMessage(msg, supportsDocuments, modelID)
		result = append(result, items...)
	}

	return result
}

func convertMessage(msg core.Message, supportsDocuments bool, modelID string) []map[string]any {
	switch msg.Role {
	case "user":
		return []map[string]any{
			{
				"role":    "user",
				"content": convertUserContent(msg.Content, supportsDocuments),
			},
		}

	case "assistant":
		return convertAssistantMessage(msg, modelID)

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
//
// Round-trip fidelity matters: the API keeps no server-side state (store:false),
// so replaying an assistant turn means reconstructing each output item as
// faithfully as the model emitted it. In particular the message item's id/phase
// and the function_call item's id are preserved — OpenAI documents that dropping
// them on manual replay causes "early stopping and other misbehavior" (empty or
// stalled turns on later requests).
//
// modelID is the current request's model. When an assistant message in history
// was produced by a DIFFERENT model (the user switched models mid-session), the
// provider-assigned output-item ids (message "id", function_call "fc_...")
// belong to that other model's response and OpenAI's pairing validation can
// reject them. In that case we omit the ids (keeping call_id/name/args/text) —
// the same conservative choice pi makes. Legacy messages with no recorded model
// keep the prior behavior.
func convertAssistantMessage(msg core.Message, modelID string) []map[string]any {
	// Foreign model: message carries a model tag that differs from the target.
	// Empty msg.Model (legacy/unknown) is treated as same-model.
	foreignModel := msg.Model != "" && modelID != "" && msg.Model != modelID

	var items []map[string]any

	for _, c := range msg.Content {
		switch c.Type {
		case "text":
			m := map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": c.Text, "annotations": []any{}},
				},
				"status": "completed",
			}
			if id, phase := parseTextSignature(c.TextSignature); id != "" || phase != "" {
				if id != "" && !foreignModel {
					m["id"] = id
				}
				if phase != "" {
					m["phase"] = phase
				}
			}
			items = append(items, m)

		case "tool_call":
			args := c.Arguments
			if args == nil {
				args = map[string]any{}
			}
			argsJSON, _ := json.Marshal(args)
			fc := map[string]any{
				"type":      "function_call",
				"call_id":   c.ToolCallID,
				"name":      c.ToolName,
				"arguments": string(argsJSON),
			}
			if c.ToolCallItemID != "" && !foreignModel {
				fc["id"] = c.ToolCallItemID
			}
			items = append(items, fc)

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
func convertUserContent(blocks []core.Content, supportsDocuments bool) []map[string]any {
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
		case "document":
			if !supportsDocuments {
				// Provider (e.g. codex OAuth) can't accept native documents.
				// Degrade to a text note so the block is never silently
				// dropped, even if it was persisted while a document-capable
				// provider was active and the user later switched.
				name := b.Filename
				if name == "" {
					name = "document"
				}
				parts = append(parts, map[string]any{
					"type": "input_text",
					"text": "[Documento adjunto \"" + name + "\" no reenviado: el proveedor actual no soporta documentos nativos.]",
				})
				continue
			}
			parts = append(parts, map[string]any{
				"type":      "input_file",
				"filename":  b.Filename,
				"file_data": "data:" + b.MimeType + ";base64," + b.Data,
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
