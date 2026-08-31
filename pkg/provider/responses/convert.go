// Package responses implements the provider-neutral Responses API wire codec.
package responses

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

// responsesRequest is the JSON body for POST /v1/responses (or /codex/responses).
type responsesRequest struct {
	Model             string           `json:"model"`
	Input             []map[string]any `json:"input"`
	Instructions      string           `json:"instructions,omitempty"`
	Tools             []map[string]any `json:"tools,omitempty"`
	Stream            bool             `json:"stream"`
	Store             bool             `json:"store"`
	MaxTokens         *int             `json:"max_output_tokens,omitempty"`
	Temperature       *float64         `json:"temperature,omitempty"`
	Reasoning         *reasoning       `json:"reasoning,omitempty"`
	ToolChoice        string           `json:"tool_choice,omitempty"`
	Include           []string         `json:"include,omitempty"`
	ParallelToolCalls bool             `json:"parallel_tool_calls,omitempty"`
	// PromptCacheKey groups requests that share a prefix so they reach the
	// same cache. Omitted when empty: an empty string would lump every
	// unidentified request into one routing group.
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
	// PromptCacheOptions selects implicit vs explicit-only caching on GPT-5.6+.
	// Omitted on earlier models and on xAI, which reject the field.
	PromptCacheOptions *promptCacheOptions `json:"prompt_cache_options,omitempty"`
}

type promptCacheOptions struct {
	Mode string `json:"mode,omitempty"`
}

type reasoning struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

// Dialect describes the validated Responses API capabilities of a transport.
// It intentionally contains protocol features only; URLs, credentials and retry
// policies stay in the owning provider.
type Dialect struct {
	Provider                         string
	Model                            string
	SupportsDocuments                bool
	SupportsMaxOutputTokens          bool
	SupportsParallelToolCalls        bool
	SupportsPromptCacheKey           bool
	SupportsExplicitCacheBreakpoints bool
	AllowedReasoningEfforts          []string
}

// BuildRequestBody encodes a stateless streaming Responses API request.
func BuildRequestBody(req core.Request, dialect Dialect) ([]byte, error) {
	r := responsesRequest{
		Model:             req.Model.ID,
		Stream:            true,
		Store:             false,
		Instructions:      req.System,
		Include:           []string{"reasoning.encrypted_content"},
		ParallelToolCalls: dialect.SupportsParallelToolCalls,
	}

	r.Input = convertMessages(req.Messages, dialect)

	if dialect.SupportsExplicitCacheBreakpoints {
		// GPT-5.6+ places a single implicit breakpoint at the latest user/tool
		// message. A mismatch anywhere in the prefix then misses tools,
		// instructions and history together. Explicit breakpoints keep those
		// stable prefixes readable; implicit mode is kept so the latest
		// message still writes. Top-level `instructions` cannot carry a
		// breakpoint, so the system prompt moves into a developer input item.
		//
		// Markers are cheap to declare: OpenAI considers up to 50 of them for
		// reads. Only four writes are created per request (implicit uses one),
		// so extra markers do not each incur a cache-write charge. Marking
		// every user/tool boundary is what the caching guide recommends for
		// multi-turn agents, so a later mismatch still hits the longest
		// surviving prefix.
		r.PromptCacheOptions = &promptCacheOptions{Mode: "implicit"}
		if r.Instructions != "" {
			r.Input = prependDeveloperInstructions(r.Input, r.Instructions)
			r.Instructions = ""
		}
	}

	if len(req.Tools) > 0 {
		r.Tools = convertToolSpecs(req.Tools)
		r.ToolChoice = "auto"
	}

	// The public Responses API (/v1/responses) accepts max_output_tokens; the
	// ChatGPT OAuth backend (/codex/responses) rejects it with HTTP 400
	// ("Unsupported parameter: max_output_tokens"). Only send the cap where it's
	// supported.
	if dialect.SupportsMaxOutputTokens {
		maxTokens := core.ResolveMaxOutputTokens(req.Model, req.Options.MaxTokens)
		r.MaxTokens = &maxTokens
	}
	if req.Options.Temperature != nil {
		r.Temperature = req.Options.Temperature
	}

	// Cache entries live on specific machines, so both OpenAI and xAI use this
	// key to route requests that share a prefix to the same one. It only
	// influences routing — the prefix must still match for a cache read.
	if dialect.SupportsPromptCacheKey {
		r.PromptCacheKey = req.Options.PromptCacheKey
	}

	if effort := MapReasoningEffort(req.Options.ThinkingLevel, dialect.AllowedReasoningEfforts); effort != "" {
		r.Reasoning = &reasoning{Effort: effort, Summary: "auto"}
	}

	return json.Marshal(r)
}

// mapReasoningEffort maps our thinking levels to OpenAI reasoning effort.
// OpenAI supports: none, minimal, low, medium, high, xhigh.
func MapReasoningEffort(level string, allowed []string) string {
	var effort string
	switch strings.ToLower(level) {
	case "off", "none", "":
		effort = ""
	case "minimal":
		effort = "minimal"
	case "low":
		effort = "low"
	case "medium":
		effort = "medium"
	case "high":
		effort = "high"
	case "xhigh":
		effort = "xhigh"
	default:
		effort = "medium"
	}
	if effort == "" || len(allowed) == 0 {
		return effort
	}
	for _, candidate := range allowed {
		if effort == candidate {
			return effort
		}
	}
	return ""
}

// convertMessages maps core messages to Responses API input format.
// dialect.SupportsDocuments gates native "document" blocks: when false (e.g.
// the codex OAuth path), any persisted document block is degraded to a text
// note instead of being emitted as an input_file the provider would reject or
// silently drop. dialect.Model is the target model of THIS request; assistant
// items produced by a different model omit their provider-assigned output-item
// ids to avoid pairing validation errors (see convertAssistantMessage).
func convertMessages(msgs []core.Message, dialect Dialect) []map[string]any {
	var result []map[string]any

	for i, msg := range msgs {
		items := convertMessageForDialect(msg, dialect, i)
		result = append(result, items...)
	}

	return result
}

func convertMessageForDialect(msg core.Message, dialect Dialect, msgIndex int) []map[string]any {
	switch msg.Role {
	case "user":
		content := convertUserContent(msg.Content, dialect.SupportsDocuments)
		if dialect.SupportsExplicitCacheBreakpoints {
			markLastInputTextBreakpoint(content)
		}
		return []map[string]any{
			{
				"role":    "user",
				"content": content,
			},
		}

	case "assistant":
		return convertAssistantMessageForDialect(msg, dialect.Provider, dialect.Model, msgIndex)

	case "tool_result":
		text := extractTextParts(msg.Content)
		item := map[string]any{
			"type":    "function_call_output",
			"call_id": msg.ToolCallID,
		}
		if dialect.SupportsExplicitCacheBreakpoints {
			// Array form is what lets a tool result carry a breakpoint; the
			// string form used without the flag is still accepted by the API.
			item["output"] = []map[string]any{
				{
					"type":                    "input_text",
					"text":                    text,
					"prompt_cache_breakpoint": explicitCacheBreakpoint(),
				},
			}
		} else {
			item["output"] = text
		}
		return []map[string]any{item}

	case "system":
		// A mid-conversation notice, rendered as a user turn for the same
		// reason as in the Anthropic conversion: placement rules for a real
		// system role vary per provider, and the notice carries its own marker
		// in the text, so the model can tell it apart without the role.
		content := convertUserContent(msg.Content, dialect.SupportsDocuments)
		if dialect.SupportsExplicitCacheBreakpoints {
			markLastInputTextBreakpoint(content)
		}
		return []map[string]any{
			{
				"role":    "user",
				"content": content,
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
// function_call's provider-assigned fc_ id belongs to that other model's
// response and OpenAI's reasoning-pairing validation can reject it, so it is
// omitted (keeping call_id/name/args). Message items instead get a stable
// synthetic id (msgIndex-based) when no real signature id is available or the
// message is cross-model — a message id is not pairing-validated, and always
// sending one reduces early stopping (matches pi).
func convertAssistantMessageForDialect(msg core.Message, provider, modelID string, msgIndex int) []map[string]any {
	// Responses messages written before model provenance was added still carry
	// their provider. Preserve that known-provider legacy metadata, while
	// continuing to reject unknown-provider and explicitly cross-model state.
	sameOrigin := msg.Provider != "" && modelID != "" && msg.Provider == provider && (msg.Model == "" || msg.Model == modelID)
	foreignModel := !sameOrigin

	var items []map[string]any
	textBlockIndex := 0

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
			id, phase := ParseTextSignature(c.TextSignature)
			// Message items should always carry an id: OpenAI documents that
			// replaying assistant text without it contributes to early
			// stopping. When we have a real signature id (same model), use it;
			// otherwise synthesize a stable id from the message position — the
			// same fallback pi uses (openai-responses-shared.ts:188). Unlike a
			// function_call's fc_ id, a message id is not subject to reasoning
			// pairing validation, so a synthetic one is safe even cross-model.
			// (phase is not recoverable when absent, so it stays omitted.)
			if id == "" || foreignModel {
				if textBlockIndex == 0 {
					id = fmt.Sprintf("msg_moa_%d", msgIndex)
				} else {
					id = fmt.Sprintf("msg_moa_%d_%d", msgIndex, textBlockIndex)
				}
			}
			m["id"] = id
			if phase != "" {
				m["phase"] = phase
			}
			textBlockIndex++
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
			// Encrypted reasoning is provider/model-bound. Replaying it across
			// providers or models is invalid and leaks opaque provider state.
			if foreignModel {
				continue
			}
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
			// The recorded media type can disagree with the bytes (e.g. a GIF
			// read from a .png). Declare what the bytes actually are: history
			// is portable across providers, and a data URL that lies about its
			// type is what Anthropic rejects outright.
			parts = append(parts, map[string]any{
				"type":      "input_image",
				"detail":    "auto",
				"image_url": "data:" + core.CorrectImageMime(b.Data, b.MimeType) + ";base64," + b.Data,
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

func explicitCacheBreakpoint() map[string]any {
	return map[string]any{"mode": "explicit"}
}

func prependDeveloperInstructions(input []map[string]any, text string) []map[string]any {
	dev := map[string]any{
		"role": "developer",
		"content": []map[string]any{
			{
				"type":                    "input_text",
				"text":                    text,
				"prompt_cache_breakpoint": explicitCacheBreakpoint(),
			},
		},
	}
	out := make([]map[string]any, 0, len(input)+1)
	out = append(out, dev)
	return append(out, input...)
}

func markLastInputTextBreakpoint(parts []map[string]any) {
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i]["type"] == "input_text" {
			parts[i]["prompt_cache_breakpoint"] = explicitCacheBreakpoint()
			return
		}
	}
}
