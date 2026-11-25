// Package antigravity runs a Google Antigravity session (the user's own,
// unmodified `agy acp`, signed in with their personal plan) behind a moa
// session.
//
// Antigravity remains the coding agent: its system prompt, tools and history
// stay inside agy, which also loads the project's AGENTS.md by itself. This
// provider never forwards moa's system prompt or tool definitions; it sends
// agy the new user messages of the session and mirrors what agy does back
// into moa's transcript: text, thinking, and each agy tool call as a card the
// provider itself resolves (ProviderTool). Permission requests from agy go
// through moa's permission gate as an approval card.
//
// It identifies honestly (clientInfo "moa"), never touches agy's credentials
// or ~/.gemini, and adds nothing to agy's system prompt.
package antigravity

import (
	"encoding/json"
	"strings"

	"github.com/e-aleixandre/moa/pkg/core"
)

// ProviderName is the moa provider id of Antigravity models.
const ProviderName = "antigravity"

// Tool names of the mirrored agy tool calls. MirrorTool is a call agy runs on
// its own authority (moa only watches it); ApprovalTool is a call agy asks
// the user about, so moa's permission gate prompts for it.
const (
	MirrorTool   = "antigravity"
	ApprovalTool = "antigravity_approval"
)

// modelPrefix namespaces agy's model values among moa's model ids: agy also
// offers models (Claude) whose ids moa already knows from other providers.
const modelPrefix = "agy."

// forYouPrefix opens the For you snapshot moa adds for its own agent loop.
// It is moa's state for moa's model, not something the user said to agy.
const forYouPrefix = "<foryou_snapshot>"

func sessionIDFromKey(key string) string {
	id, ok := strings.CutPrefix(key, "moa:session:")
	if !ok || strings.Contains(id, ":") {
		return "" // a subagent's key extends its parent's
	}
	return id
}

// promptBlocks converts the new user messages into one ACP prompt.
func promptBlocks(msgs []core.Message) []any {
	var out []any
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		for _, c := range m.Content {
			switch c.Type {
			case "text":
				if c.Text != "" && !strings.HasPrefix(c.Text, forYouPrefix) {
					out = append(out, map[string]any{"type": "text", "text": c.Text})
				}
			case "image":
				out = append(out, map[string]any{"type": "image", "mimeType": c.MimeType, "data": c.Data})
			}
		}
	}
	return out
}

func rawString(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	return string(v)
}
