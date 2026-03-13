package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
)

// Claude Code identity — required for OAuth tokens (Claude Max).
const (
	claudeCodeVersion        = "2.1.62"
	claudeCodeSystemPreamble = "You are Claude Code, Anthropic's official CLI for Claude."
)

// Claude Code canonical tool names (must match exactly for OAuth).
var claudeCodeTools = []string{
	"Read", "Write", "Edit", "Bash", "Grep", "Glob",
	"AskUserQuestion", "EnterPlanMode", "ExitPlanMode",
	"KillShell", "NotebookEdit", "Skill", "Task",
	"TaskOutput", "TodoWrite", "WebFetch", "WebSearch",
}

var ccToolLookup = func() map[string]string {
	m := make(map[string]string, len(claudeCodeTools))
	for _, t := range claudeCodeTools {
		m[strings.ToLower(t)] = t
	}
	return m
}()

// toClaudeCodeName maps a tool name to Claude Code's canonical casing.
// If the tool doesn't match a known CC tool name, it's returned as-is.
func toClaudeCodeName(name string) string {
	if cc, ok := ccToolLookup[strings.ToLower(name)]; ok {
		return cc
	}
	return name
}

// fromClaudeCodeName maps a CC tool name back to the original name
// by looking up the original tool specs.
func fromClaudeCodeName(name string, specs []core.ToolSpec) string {
	lower := strings.ToLower(name)
	for _, s := range specs {
		if strings.ToLower(s.Name) == lower {
			return s.Name
		}
	}
	return name
}

// anthropicRequest is the JSON body for POST /v1/messages.
type anthropicRequest struct {
	Model        string           `json:"model"`
	System       any              `json:"system,omitempty"`
	Messages     []map[string]any `json:"messages"`
	Tools        []map[string]any `json:"tools,omitempty"`
	MaxTokens    int              `json:"max_tokens"`
	Stream       bool             `json:"stream"`
	Thinking     *thinkingConfig  `json:"thinking,omitempty"`
	OutputConfig *outputConfig    `json:"output_config,omitempty"`
}

type thinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

type outputConfig struct {
	Effort string `json:"effort,omitempty"`
}

// buildRequestBody converts a core.Request to Anthropic API JSON bytes.
// If isOAuth is true, the system prompt is prefixed with the Claude Code
// identity string and tool names are mapped to Claude Code's canonical casing.
func buildRequestBody(req core.Request, isOAuth bool) ([]byte, error) {
	ar := anthropicRequest{
		Model:     req.Model.ID,
		MaxTokens: resolveMaxTokens(req),
		Stream:    true,
	}

	// System prompt — OAuth requires Claude Code preamble
	if isOAuth {
		systemBlocks := []map[string]any{
			{"type": "text", "text": claudeCodeSystemPreamble},
		}
		if req.System != "" {
			systemBlocks = append(systemBlocks, map[string]any{
				"type": "text", "text": req.System,
			})
		}
		ar.System = systemBlocks
	} else if req.System != "" {
		ar.System = []map[string]any{
			{"type": "text", "text": req.System},
		}
	}

	// Messages — merge consecutive same-role messages (Anthropic requires alternation)
	ar.Messages = convertMessages(req.Messages, isOAuth)

	// Tools — remap names for OAuth
	if len(req.Tools) > 0 {
		ar.Tools = convertToolSpecs(req.Tools, isOAuth)
	}

	// Thinking
	if supportsAdaptiveThinking(req.Model.ID) {
		if effort := resolveEffort(req.Options.ThinkingLevel, req.Model.ID); effort != "" {
			ar.Thinking = &thinkingConfig{Type: "adaptive"}
			ar.OutputConfig = &outputConfig{Effort: effort}
		}
	} else if t := resolveThinking(req); t != nil {
		ar.Thinking = t
		// Anthropic manual thinking requires: max_tokens > budget_tokens.
		minMaxTokens := t.BudgetTokens + 1
		if minMaxTokens < 16000 {
			minMaxTokens = 16000
		}
		if ar.MaxTokens < minMaxTokens {
			ar.MaxTokens = minMaxTokens
		}
	}

	// Prompt caching — add cache_control breakpoints.
	// Anthropic caches everything up to each breakpoint. Three breakpoints:
	// 1. Last system block (system prompt is identical turn-to-turn)
	// 2. Last tool definition (tool specs are identical turn-to-turn)
	// 3. Last content block of the last user message (caches conversation history)
	addCacheBreakpoints(&ar)

	return json.Marshal(ar)
}

// convertMessages maps core.Message slice to Anthropic API format.
//
// Mapping:
//
//	core.Message{Role:"user"}        → {"role":"user","content":[...]}
//	core.Message{Role:"assistant"}   → {"role":"assistant","content":[...]}
//	core.Message{Role:"tool_result"} → {"role":"user","content":[{"type":"tool_result",...}]}
//
// Consecutive messages with the same Anthropic role are merged into one.
func convertMessages(msgs []core.Message, isOAuth bool) []map[string]any {
	var result []map[string]any

	for _, msg := range msgs {
		apiMsg := convertMessage(msg, isOAuth)
		if apiMsg == nil {
			continue
		}

		// Merge consecutive same-role messages
		if len(result) > 0 {
			last := result[len(result)-1]
			if last["role"] == apiMsg["role"] {
				lastContent, _ := last["content"].([]any)
				newContent, _ := apiMsg["content"].([]any)
				last["content"] = append(lastContent, newContent...)
				continue
			}
		}

		result = append(result, apiMsg)
	}

	return result
}

// convertMessage maps a single core.Message to Anthropic API format.
func convertMessage(msg core.Message, isOAuth bool) map[string]any {
	switch msg.Role {
	case "user":
		return map[string]any{
			"role":    "user",
			"content": convertContentBlocks(msg.Content),
		}

	case "assistant":
		return map[string]any{
			"role":    "assistant",
			"content": convertAssistantContent(msg.Content, isOAuth),
		}

	case "tool_result":
		// Anthropic: tool results are user messages with tool_result content blocks
		block := map[string]any{
			"type":        "tool_result",
			"tool_use_id": msg.ToolCallID,
		}
		if msg.IsError {
			block["is_error"] = true
		}
		if len(msg.Content) > 0 {
			block["content"] = convertContentBlocks(msg.Content)
		}
		return map[string]any{
			"role":    "user",
			"content": []any{block},
		}

	default:
		return nil // Skip unknown roles
	}
}

// convertContentBlocks converts core.Content slices to Anthropic content blocks.
func convertContentBlocks(blocks []core.Content) []any {
	result := make([]any, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			result = append(result, map[string]any{
				"type": "text",
				"text": b.Text,
			})
		case "image":
			result = append(result, map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": b.MimeType,
					"data":       b.Data,
				},
			})
		}
	}
	return result
}

// convertAssistantContent converts assistant message content including tool calls and thinking.
func convertAssistantContent(blocks []core.Content, isOAuth bool) []any {
	result := make([]any, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			result = append(result, map[string]any{
				"type": "text",
				"text": b.Text,
			})
		case "thinking":
			block := map[string]any{
				"type":     "thinking",
				"thinking": b.Thinking,
			}
			if b.ThinkingSignature != "" {
				block["signature"] = b.ThinkingSignature
			}
			if b.Redacted {
				block["type"] = "redacted_thinking"
				delete(block, "thinking")
			}
			result = append(result, block)
		case "tool_call":
			name := b.ToolName
			if isOAuth {
				name = toClaudeCodeName(name)
			}
			input := any(b.Arguments)
			if b.Arguments == nil {
				input = map[string]any{}
			}
			result = append(result, map[string]any{
				"type":  "tool_use",
				"id":    b.ToolCallID,
				"name":  name,
				"input": input,
			})
		}
	}
	return result
}

// convertToolSpecs maps []core.ToolSpec to Anthropic's tool format.
func convertToolSpecs(specs []core.ToolSpec, isOAuth bool) []map[string]any {
	result := make([]map[string]any, 0, len(specs))
	for _, s := range specs {
		name := s.Name
		if isOAuth {
			name = toClaudeCodeName(name)
		}
		t := map[string]any{
			"name":        name,
			"description": s.Description,
		}
		if len(s.Parameters) > 0 {
			var schema any
			if err := json.Unmarshal(s.Parameters, &schema); err == nil {
				// Verify it's an object-like schema (Anthropic requires object)
				if _, ok := schema.(map[string]any); ok {
					t["input_schema"] = schema
				} else {
					t["input_schema"] = map[string]any{"type": "object"}
				}
			} else {
				// Parse failure: fallback to empty object
				t["input_schema"] = map[string]any{"type": "object"}
			}
		} else {
			// Anthropic requires input_schema; use empty object
			t["input_schema"] = map[string]any{"type": "object"}
		}
		result = append(result, t)
	}
	return result
}

func resolveMaxTokens(req core.Request) int {
	if req.Options.MaxTokens != nil {
		return *req.Options.MaxTokens
	}
	return 8192
}

// supportsAdaptiveThinking reports whether a model supports Anthropic adaptive
// thinking (Opus 4.6 and Sonnet 4.6).
func supportsAdaptiveThinking(modelID string) bool {
	id := strings.ToLower(modelID)
	return strings.Contains(id, "opus-4-6") ||
		strings.Contains(id, "opus-4.6") ||
		strings.Contains(id, "sonnet-4-6") ||
		strings.Contains(id, "sonnet-4.6")
}

// resolveEffort maps our thinking levels to Anthropic adaptive effort.
func resolveEffort(level, modelID string) string {
	switch strings.ToLower(level) {
	case "", "off", "none":
		return ""
	case "minimal", "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh":
		id := strings.ToLower(modelID)
		if strings.Contains(id, "opus-4-6") || strings.Contains(id, "opus-4.6") {
			return "max"
		}
		return "high"
	default:
		return "medium"
	}
}

// resolveThinking maps thinking level to Anthropic manual thinking config.
func resolveThinking(req core.Request) *thinkingConfig {
	switch strings.ToLower(req.Options.ThinkingLevel) {
	case "minimal":
		return &thinkingConfig{Type: "enabled", BudgetTokens: 1024}
	case "low":
		return &thinkingConfig{Type: "enabled", BudgetTokens: 4096}
	case "medium":
		return &thinkingConfig{Type: "enabled", BudgetTokens: 10000}
	case "high":
		return &thinkingConfig{Type: "enabled", BudgetTokens: 32000}
	default:
		return nil // "off", "none", or empty
	}
}

var cacheControl = map[string]string{"type": "ephemeral"}

// addCacheBreakpoints marks the last system block, last tool, and last user
// message content block with cache_control for Anthropic prompt caching.
func addCacheBreakpoints(ar *anthropicRequest) {
	// 1. Last system block
	if blocks, ok := ar.System.([]map[string]any); ok && len(blocks) > 0 {
		blocks[len(blocks)-1]["cache_control"] = cacheControl
	}

	// 2. Last tool
	if len(ar.Tools) > 0 {
		ar.Tools[len(ar.Tools)-1]["cache_control"] = cacheControl
	}

	// 3. Last content block of the final user message (caches conversation history).
	// Walk backwards to find the last user-role message.
	for i := len(ar.Messages) - 1; i >= 0; i-- {
		if ar.Messages[i]["role"] == "user" {
			if content, ok := ar.Messages[i]["content"].([]any); ok && len(content) > 0 {
				if block, ok := content[len(content)-1].(map[string]any); ok {
					block["cache_control"] = cacheControl
				}
			}
			break
		}
	}
}
