package responses

import (
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func gpt56Dialect() Dialect {
	return Dialect{Provider: "openai", Model: "gpt-5.6-terra", SupportsPromptCacheKey: true, SupportsExplicitCacheBreakpoints: true}
}

func TestBuildRequestBody_ExplicitBreakpoints(t *testing.T) {
	req := core.Request{
		Model:  core.Model{ID: "gpt-5.6-terra"},
		System: "You are helpful.",
		Messages: []core.Message{
			core.NewUserMessage("first"),
			{Role: "assistant", Provider: "openai", Model: "gpt-5.6-terra", Content: []core.Content{
				core.ToolCallContent("call-1", "bash", map[string]any{"command": "ls"}),
			}},
			core.NewToolResultMessage("call-1", "bash", []core.Content{core.TextContent("ok")}, false),
			core.NewUserMessage("second"),
		},
	}

	body := decodeBody(t, req, gpt56Dialect())

	if _, ok := body["instructions"]; ok {
		t.Fatalf("instructions stayed top-level; GPT-5.6 cannot breakpoint that field: %v", body["instructions"])
	}
	opts, _ := body["prompt_cache_options"].(map[string]any)
	if opts["mode"] != "implicit" {
		t.Fatalf("prompt_cache_options.mode = %v, want implicit", opts["mode"])
	}

	input, _ := body["input"].([]any)
	if len(input) < 4 {
		t.Fatalf("input len = %d, want developer + conversation", len(input))
	}

	dev, _ := input[0].(map[string]any)
	if dev["role"] != "developer" {
		t.Fatalf("first item role = %v, want developer", dev["role"])
	}
	devParts, _ := dev["content"].([]any)
	if len(devParts) != 1 {
		t.Fatalf("developer parts = %d, want 1", len(devParts))
	}
	devPart, _ := devParts[0].(map[string]any)
	if devPart["text"] != "You are helpful." {
		t.Fatalf("developer text = %v", devPart["text"])
	}
	if mode := breakpointMode(devPart); mode != "explicit" {
		t.Fatalf("developer breakpoint = %q, want explicit", mode)
	}

	userBreakpoints := 0
	toolBreakpoints := 0
	for _, raw := range input[1:] {
		item, _ := raw.(map[string]any)
		switch {
		case item["role"] == "user":
			parts, _ := item["content"].([]any)
			if len(parts) == 0 {
				t.Fatal("user message has no content")
			}
			last, _ := parts[len(parts)-1].(map[string]any)
			if mode := breakpointMode(last); mode != "explicit" {
				t.Fatalf("user breakpoint = %q, want explicit", mode)
			}
			userBreakpoints++
		case item["type"] == "function_call_output":
			parts, ok := item["output"].([]any)
			if !ok {
				t.Fatalf("tool output stayed a string: %T %v", item["output"], item["output"])
			}
			if len(parts) != 1 {
				t.Fatalf("tool output parts = %d, want 1", len(parts))
			}
			part, _ := parts[0].(map[string]any)
			if part["text"] != "ok" {
				t.Fatalf("tool output text = %v", part["text"])
			}
			if mode := breakpointMode(part); mode != "explicit" {
				t.Fatalf("tool breakpoint = %q, want explicit", mode)
			}
			toolBreakpoints++
		}
	}
	if userBreakpoints != 2 {
		t.Fatalf("user breakpoints = %d, want 2", userBreakpoints)
	}
	if toolBreakpoints != 1 {
		t.Fatalf("tool breakpoints = %d, want 1", toolBreakpoints)
	}
}

func TestBuildRequestBody_ExplicitBreakpointsOff(t *testing.T) {
	req := core.Request{
		Model:    core.Model{ID: "gpt-5.5"},
		System:   "You are helpful.",
		Messages: []core.Message{core.NewUserMessage("hello")},
		Options:  core.StreamOptions{PromptCacheKey: "moa:session:abc"},
	}
	raw, err := BuildRequestBody(req, Dialect{Provider: "openai", Model: req.Model.ID, SupportsPromptCacheKey: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "prompt_cache_breakpoint") || strings.Contains(string(raw), "prompt_cache_options") {
		t.Fatalf("flag-off body leaked explicit cache fields: %s", raw)
	}
	body := decodeBody(t, req, Dialect{Provider: "openai", Model: req.Model.ID, SupportsPromptCacheKey: true})
	if body["instructions"] != "You are helpful." {
		t.Fatalf("instructions = %v, want top-level", body["instructions"])
	}
	if body["prompt_cache_key"] != "moa:session:abc" {
		t.Fatalf("prompt_cache_key = %v", body["prompt_cache_key"])
	}
}

func TestBuildRequestBody_ExplicitBreakpointsNotSentToXAI(t *testing.T) {
	req := core.Request{
		Model:    core.Model{ID: "grok-4.6"},
		System:   "You are helpful.",
		Messages: []core.Message{core.NewUserMessage("hello")},
		Options:  core.StreamOptions{PromptCacheKey: "moa:session:abc"},
	}
	raw, err := BuildRequestBody(req, Dialect{Provider: "xai", Model: req.Model.ID, SupportsPromptCacheKey: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "prompt_cache_breakpoint") || strings.Contains(string(raw), "prompt_cache_options") {
		t.Fatalf("xAI body leaked OpenAI cache fields: %s", raw)
	}
}

func TestBuildRequestBody_ImageOnlyUserHasNoBreakpoint(t *testing.T) {
	req := core.Request{
		Model:  core.Model{ID: "gpt-5.6-terra"},
		System: "sys",
		Messages: []core.Message{
			core.NewUserMessageWithContent([]core.Content{
				core.ImageContent("AAAA", "image/png"),
			}),
		},
	}
	body := decodeBody(t, req, gpt56Dialect())
	input, _ := body["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("input len = %d, want developer + user", len(input))
	}
	user, _ := input[1].(map[string]any)
	parts, _ := user["content"].([]any)
	if len(parts) != 1 {
		t.Fatalf("user parts = %d", len(parts))
	}
	part, _ := parts[0].(map[string]any)
	if part["type"] != "input_image" {
		t.Fatalf("part type = %v, want input_image", part["type"])
	}
	if _, ok := part["prompt_cache_breakpoint"]; ok {
		t.Fatalf("image part carried a breakpoint: %v", part)
	}
}

func breakpointMode(part map[string]any) string {
	bp, _ := part["prompt_cache_breakpoint"].(map[string]any)
	mode, _ := bp["mode"].(string)
	return mode
}
