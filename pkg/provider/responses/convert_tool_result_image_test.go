package responses

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func imageDialect(explicitCache bool) Dialect {
	return Dialect{
		Provider: "openai", Model: "gpt-5.6-terra",
		SupportsExplicitCacheBreakpoints: explicitCache,
	}
}

func toolOutputParts(t *testing.T, msg core.Message, dialect Dialect) []map[string]any {
	t.Helper()
	items := convertMessageForDialect(msg, dialect, 0)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	parts, ok := items[0]["output"].([]map[string]any)
	if !ok {
		t.Fatalf("output is not an array: %T %v", items[0]["output"], items[0]["output"])
	}
	return parts
}

// A read/MCP tool result whose only block is an image must reach the model as
// an input_image: joining text blocks alone left it with nothing to look at.
func TestToolResultImageOnlyBecomesInputImage(t *testing.T) {
	msg := core.NewToolResultMessage("call-1", "read", []core.Content{
		core.ImageContent("AAAA", "image/png"),
	}, false)

	for _, explicit := range []bool{false, true} {
		parts := toolOutputParts(t, msg, imageDialect(explicit))
		if len(parts) != 1 {
			t.Fatalf("explicitCache=%v: parts = %d, want 1", explicit, len(parts))
		}
		if parts[0]["type"] != "input_image" {
			t.Fatalf("explicitCache=%v: part type = %v", explicit, parts[0]["type"])
		}
		url, _ := parts[0]["image_url"].(string)
		if !strings.HasPrefix(url, "data:") || !strings.Contains(url, ";base64,AAAA") {
			t.Fatalf("explicitCache=%v: image_url = %q", explicit, url)
		}
		if _, ok := parts[0]["prompt_cache_breakpoint"]; ok {
			t.Fatalf("explicitCache=%v: image part carried a breakpoint", explicit)
		}
	}
}

// Mixed results keep both kinds of block, in the order the tool produced them.
func TestToolResultMixedPreservesOrder(t *testing.T) {
	msg := core.NewToolResultMessage("call-1", "read", []core.Content{
		core.TextContent("before"),
		core.ImageContent("AAAA", "image/png"),
		core.TextContent("after"),
	}, false)

	parts := toolOutputParts(t, msg, imageDialect(false))
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want 3", len(parts))
	}
	if parts[0]["type"] != "input_text" || parts[0]["text"] != "before" {
		t.Fatalf("part 0 = %v", parts[0])
	}
	if parts[1]["type"] != "input_image" {
		t.Fatalf("part 1 = %v", parts[1])
	}
	if parts[2]["type"] != "input_text" || parts[2]["text"] != "after" {
		t.Fatalf("part 2 = %v", parts[2])
	}
}

// With explicit caching on, exactly one breakpoint is written, and it lands on
// the last text part, matching the existing user-message breakpoint policy.
func TestToolResultMixedBreakpointOnLastText(t *testing.T) {
	msg := core.NewToolResultMessage("call-1", "read", []core.Content{
		core.TextContent("before"),
		core.ImageContent("AAAA", "image/png"),
		core.TextContent("after"),
	}, false)

	parts := toolOutputParts(t, msg, imageDialect(true))
	breakpoints := 0
	for _, part := range parts {
		if _, ok := part["prompt_cache_breakpoint"]; ok {
			breakpoints++
			if part["text"] != "after" {
				t.Fatalf("breakpoint landed on %v", part)
			}
		}
	}
	if breakpoints != 1 {
		t.Fatalf("breakpoints = %d, want 1", breakpoints)
	}
}

// An image-only result under explicit caching writes no breakpoint at all
// rather than inventing a text part to carry one.
func TestToolResultImageOnlyHasNoBreakpoint(t *testing.T) {
	msg := core.NewToolResultMessage("call-1", "read", []core.Content{
		core.ImageContent("AAAA", "image/png"),
	}, false)
	parts := toolOutputParts(t, msg, imageDialect(true))
	for _, part := range parts {
		if _, ok := part["prompt_cache_breakpoint"]; ok {
			t.Fatalf("image-only output carried a breakpoint: %v", part)
		}
	}
}

// Text-only results must keep their exact previous wire shape so prefixes
// cached before this change still match.
func TestToolResultTextOnlyShapeUnchanged(t *testing.T) {
	msg := core.NewToolResultMessage("call-1", "bash", []core.Content{
		core.TextContent("one"),
		core.TextContent("two"),
	}, false)

	plain := convertMessageForDialect(msg, Dialect{Provider: "openai", Model: "gpt-5.3-codex"}, 0)
	if got, ok := plain[0]["output"].(string); !ok || got != "onetwo" {
		t.Fatalf("output = %T %v, want the joined string", plain[0]["output"], plain[0]["output"])
	}

	parts := toolOutputParts(t, msg, imageDialect(true))
	if len(parts) != 1 {
		t.Fatalf("parts = %d, want a single input_text", len(parts))
	}
	if parts[0]["type"] != "input_text" || parts[0]["text"] != "onetwo" {
		t.Fatalf("part = %v", parts[0])
	}
	if breakpointMode(parts[0]) != "explicit" {
		t.Fatalf("text-only output lost its breakpoint: %v", parts[0])
	}
}

// The media type declared on the wire comes from the bytes, not from the block:
// a GIF recorded as image/png must not go out lying about itself.
func TestToolResultImageMimeCorrected(t *testing.T) {
	gif := base64.StdEncoding.EncodeToString(append([]byte("GIF89a"), make([]byte, 512)...))
	msg := core.NewToolResultMessage("call-1", "read", []core.Content{
		core.ImageContent(gif, "image/png"),
	}, false)

	parts := toolOutputParts(t, msg, imageDialect(false))
	url, _ := parts[0]["image_url"].(string)
	if !strings.HasPrefix(url, "data:image/gif;base64,") {
		t.Fatalf("image_url should declare image/gif, got %.40q", url)
	}
}

// Every transport sharing this codec was verified live to read an image
// returned inside a function_call_output, so none of them degrades it.
func TestToolResultImageForAllSharedTransports(t *testing.T) {
	msg := core.NewToolResultMessage("call-1", "read", []core.Content{
		core.TextContent("screenshot:"),
		core.ImageContent("AAAA", "image/png"),
	}, false)

	for _, dialect := range []Dialect{
		{Provider: "xai", Model: "grok-4.6"},
		{Provider: "meta", Model: "muse-spark-1.3"},
		{Provider: "openai", Model: "gpt-5.3-codex"},
	} {
		parts := toolOutputParts(t, msg, dialect)
		if len(parts) != 2 {
			t.Fatalf("%s: parts = %d, want 2", dialect.Provider, len(parts))
		}
		if parts[0]["text"] != "screenshot:" {
			t.Fatalf("%s: text block lost: %v", dialect.Provider, parts[0])
		}
		if parts[1]["type"] != "input_image" {
			t.Fatalf("%s: part 1 type = %v, want input_image", dialect.Provider, parts[1])
		}
	}
}
