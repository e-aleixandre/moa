package anthropic

import (
	"encoding/json"
	"fmt"
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
		// Anthropic manual thinking requires max_tokens > budget_tokens. Keep
		// the resolved output cap authoritative and reduce the thinking budget
		// instead, reserving room for a visible response.
		const (
			minVisibleOutputTokens  = 1024
			minThinkingBudgetTokens = 1024
		)
		maxBudget := ar.MaxTokens - minVisibleOutputTokens
		if maxBudget < minThinkingBudgetTokens {
			// A cap this small cannot satisfy Anthropic's manual-thinking
			// minimum while leaving a usable visible response. Prefer a valid
			// non-thinking request to an API-rejected one.
			ar.Thinking = nil
		} else if t.BudgetTokens > maxBudget {
			t.BudgetTokens = maxBudget
		}
	}

	// Prompt caching — add cache_control breakpoints.
	// Anthropic caches everything up to each breakpoint. Three breakpoints:
	// 1. Last system block (system prompt is identical turn-to-turn)
	// 2. Last tool definition (tool specs are identical turn-to-turn)
	// 3. Last content block of the last user message (caches conversation history)
	addCacheBreakpoints(&ar, req.Options.CacheRetention)

	return json.Marshal(ar)
}

// manyImageThreshold is the number of image blocks in a single request above
// which Anthropic drops the per-side size cap from MaxImageDimension to
// manyImageMaxDimension. Requests at or below it may carry full-size images.
const manyImageThreshold = 20

// manyImageMaxDimension is the per-side cap that applies once a request carries
// more than manyImageThreshold images. It is not enforced by measuring: any
// image can breach it, so retirement is by age, not by size.
const manyImageMaxDimension = 2000

// imageRetireBatch is how many images are retired at a time. Retiring exactly
// the overflow would change the retired set on every new image and invalidate
// the prompt cache each turn; rounding up to a batch keeps the request bytes
// stable until the count crosses the next boundary, and history is append-only,
// so between crossings the cached prefix survives.
const imageRetireBatch = 8

// imageRetirer tracks how many of the oldest image blocks still have to be
// swapped for a text note. Conversion walks messages in chronological order, so
// "the next image encountered" is always the oldest one left.
//
// A nil retirer retires nothing, which is the <= manyImageThreshold case.
type imageRetirer struct {
	remaining int
}

// newImageRetirer counts the image blocks the request would put on the wire and
// decides how many of the oldest to retire.
//
// Anthropic applies a stricter 2000 px per-side cap once a request carries more
// than 20 images, and rejects the whole request with a 400 when any image
// breaches it. History is replayed every turn, so one 1170x2532 screenshot in a
// long session poisons every following turn permanently. Retiring the oldest
// images brings the count back to the threshold, which restores the full-size
// allowance for the ones that are left; it also un-poisons a conversation that
// is already stuck, on its next turn, with no user action.
//
// Only images count here. Documents do not count toward the threshold on the
// direct API, which is the only one moa talks to; Bedrock and Vertex do count
// them, so this is the place to adjust if either is ever supported.
func newImageRetirer(msgs []core.Message) *imageRetirer {
	count := 0
	for _, msg := range msgs {
		// Assistant content never carries images (convertAssistantContent
		// drops them), and unknown roles are skipped entirely.
		if msg.Role != "user" && msg.Role != "tool_result" {
			continue
		}
		for _, b := range msg.Content {
			if b.Type != "image" {
				continue
			}
			// Images above MaxImageDimension are replaced by a note further
			// down, so they never reach the wire as image blocks and must not
			// inflate the count.
			if _, _, tooBig := core.ImageExceedsMaxDimension(b.Data); tooBig {
				continue
			}
			count++
		}
	}
	if count <= manyImageThreshold {
		return nil
	}
	overflow := count - manyImageThreshold
	batches := (overflow + imageRetireBatch - 1) / imageRetireBatch
	return &imageRetirer{remaining: batches * imageRetireBatch}
}

// takeOldest reports whether the image block being converted is one of the
// oldest ones scheduled for retirement, consuming one slot when it is.
func (r *imageRetirer) takeOldest() bool {
	if r == nil || r.remaining <= 0 {
		return false
	}
	r.remaining--
	return true
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

	// Message order is chronological, so conversion order is age order: the
	// retirer hands out its slots to the oldest images first.
	retire := newImageRetirer(msgs)

	for _, msg := range msgs {
		apiMsg := convertMessage(msg, isOAuth, retire)
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
func convertMessage(msg core.Message, isOAuth bool, retire *imageRetirer) map[string]any {
	switch msg.Role {
	case "user":
		return map[string]any{
			"role":    "user",
			"content": convertContentBlocks(msg.Content, retire),
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
			block["content"] = convertContentBlocks(msg.Content, retire)
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
// retire may be nil, meaning no image is old enough to be retired.
func convertContentBlocks(blocks []core.Content, retire *imageRetirer) []any {
	result := make([]any, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			result = append(result, map[string]any{
				"type": "text",
				"text": b.Text,
			})
		case "image":
			// A single oversized image makes every subsequent request fail with a
			// hard 400, because history is replayed each turn. Substitute a note
			// so an already-poisoned conversation stays usable; the read tool
			// rejects such images up front for anything recorded from now on.
			w, h, tooBig := core.ImageExceedsMaxDimension(b.Data)
			if tooBig {
				result = append(result, map[string]any{
					"type": "text",
					"text": fmt.Sprintf("[image omitted: %dx%d px exceeds the %d px per-side limit; "+
						"resize or split it and read it again]", w, h, core.MaxImageDimension),
				})
				continue
			}
			// Too many images in one request: the oldest ones step aside so the
			// newest keep their full resolution. Retirement is by age, not by
			// size, so the note says nothing about the limit being breached.
			if retire.takeOldest() {
				result = append(result, map[string]any{
					"type": "text",
					"text": retiredImageNote(w, h),
				})
				continue
			}
			result = append(result, map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": b.MimeType,
					"data":       b.Data,
				},
			})
		case "document":
			result = append(result, map[string]any{
				"type": "document",
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

// retiredImageNote is the text block that stands in for a retired image. The
// size is included when it could be read, since it tells the model what it is
// missing. Dimensions of 0x0 mean the header was unreadable, not a tiny image.
func retiredImageNote(w, h int) string {
	if w > 0 && h > 0 {
		return fmt.Sprintf("[image omitted: this %dx%d px image was retired because the conversation "+
			"holds more than %d images, which caps every image at %d px per side; "+
			"read the file again if you still need it]",
			w, h, manyImageThreshold, manyImageMaxDimension)
	}
	return fmt.Sprintf("[image omitted: an older image was retired because the conversation "+
		"holds more than %d images, which caps every image at %d px per side; "+
		"read the file again if you still need it]",
		manyImageThreshold, manyImageMaxDimension)
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
			if b.Redacted {
				result = append(result, map[string]any{
					"type": "redacted_thinking",
					"data": b.ThinkingSignature,
				})
			} else if strings.TrimSpace(b.Thinking) == "" {
				// Empty thinking block — skip entirely
				continue
			} else if b.ThinkingSignature == "" {
				// Thinking without signature (e.g. aborted stream) —
				// emit as plain text to avoid API rejection.
				result = append(result, map[string]any{
					"type": "text",
					"text": b.Thinking,
				})
			} else {
				result = append(result, map[string]any{
					"type":      "thinking",
					"thinking":  b.Thinking,
					"signature": b.ThinkingSignature,
				})
			}
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
	return core.ResolveMaxOutputTokens(req.Model, req.Options.MaxTokens)
}

// supportsAdaptiveThinking reports whether a model supports Anthropic adaptive
// thinking (Opus 5, Opus 4.8 and Sonnet 5). Haiku 4.5 uses manual extended thinking.
func supportsAdaptiveThinking(modelID string) bool {
	id := strings.ToLower(modelID)
	return strings.Contains(id, "opus-5") ||
		strings.Contains(id, "opus-4-8") ||
		strings.Contains(id, "opus-4.8") ||
		strings.Contains(id, "sonnet-5")
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
		// Only Opus exposes the "max" effort tier; Sonnet caps at "high".
		if strings.Contains(strings.ToLower(modelID), "opus") {
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
	case "high", "xhigh":
		// Manual-thinking models (Haiku 4.5, Fable) expose no tier above "high",
		// so "xhigh" caps here — mirroring resolveEffort, which caps non-Opus
		// "xhigh" at "high". Never fall through to default: that would return nil
		// and silently disable thinking when the *maximum* level was requested.
		return &thinkingConfig{Type: "enabled", BudgetTokens: 32000}
	default:
		return nil // "off", "none", or empty
	}
}

// addCacheBreakpoints marks the last system block, last tool, and last user
// message content block with cache_control for Anthropic prompt caching. ttl is
// the cache retention: "1h" for the extended window (2x write cost), or "" for
// the default 5-minute ephemeral cache. The three breakpoints share one map
// value; it is never mutated after assignment, so sharing the reference is safe.
func addCacheBreakpoints(ar *anthropicRequest, ttl string) {
	cc := map[string]any{"type": "ephemeral"}
	if ttl == "1h" {
		cc["ttl"] = "1h"
	}

	// 1. Last system block
	if blocks, ok := ar.System.([]map[string]any); ok && len(blocks) > 0 {
		blocks[len(blocks)-1]["cache_control"] = cc
	}

	// 2. Last tool
	if len(ar.Tools) > 0 {
		ar.Tools[len(ar.Tools)-1]["cache_control"] = cc
	}

	// 3. Last content block of the final user message (caches conversation history).
	// Walk backwards to find the last user-role message.
	for i := len(ar.Messages) - 1; i >= 0; i-- {
		if ar.Messages[i]["role"] == "user" {
			if content, ok := ar.Messages[i]["content"].([]any); ok && len(content) > 0 {
				if block, ok := content[len(content)-1].(map[string]any); ok {
					block["cache_control"] = cc
				}
			}
			break
		}
	}
}
