package openai

import (
	"encoding/json"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestBuildRequestBody_Basic(t *testing.T) {
	req := core.Request{
		Model:  core.Model{ID: "gpt-5.3-codex"},
		System: "You are helpful.",
		Messages: []core.Message{
			core.NewUserMessage("Hello"),
		},
	}

	body, err := buildRequestBody(req)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}

	if parsed["model"] != "gpt-5.3-codex" {
		t.Fatalf("model: %v", parsed["model"])
	}
	if parsed["stream"] != true {
		t.Fatal("stream should be true")
	}

	msgs, _ := parsed["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	first := msgs[0].(map[string]any)
	if first["role"] != "developer" {
		t.Fatalf("first message role: %v", first["role"])
	}
	if first["content"] != "You are helpful." {
		t.Fatalf("system content: %v", first["content"])
	}
}

func TestBuildRequestBody_WithTools(t *testing.T) {
	req := core.Request{
		Model: core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{
			core.NewUserMessage("List files"),
		},
		Tools: []core.ToolSpec{
			{
				Name:        "bash",
				Description: "Execute commands",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
			},
		},
	}

	body, err := buildRequestBody(req)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	json.Unmarshal(body, &parsed)

	tools, ok := parsed["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %v", parsed["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Fatalf("tool type: %v", tool["type"])
	}
	fn := tool["function"].(map[string]any)
	if fn["name"] != "bash" {
		t.Fatalf("tool name: %v", fn["name"])
	}
}

func TestBuildRequestBody_ReasoningEffort(t *testing.T) {
	req := core.Request{
		Model:    core.Model{ID: "gpt-5.3-codex"},
		Messages: []core.Message{core.NewUserMessage("Think hard")},
		Options:  core.StreamOptions{ThinkingLevel: "high"},
	}

	body, err := buildRequestBody(req)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	json.Unmarshal(body, &parsed)
	if parsed["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort: %v", parsed["reasoning_effort"])
	}
}

func TestConvertMessage_ToolResult(t *testing.T) {
	msg := core.NewToolResultMessage("call-1", "bash", []core.Content{core.TextContent("output")}, false)
	result := convertMessage(msg)
	if result["role"] != "tool" {
		t.Fatalf("role: %v", result["role"])
	}
	if result["tool_call_id"] != "call-1" {
		t.Fatalf("tool_call_id: %v", result["tool_call_id"])
	}
}

func TestConvertMessage_AssistantWithToolCalls(t *testing.T) {
	msg := core.Message{
		Role: "assistant",
		Content: []core.Content{
			core.TextContent("I'll run this"),
			core.ToolCallContent("tc-1", "bash", map[string]any{"command": "ls"}),
		},
	}
	result := convertMessage(msg)
	if result["content"] != "I'll run this" {
		t.Fatalf("content: %v", result["content"])
	}
	calls, ok := result["tool_calls"].([]map[string]any)
	if !ok || len(calls) != 1 {
		t.Fatal("expected 1 tool call")
	}
	if calls[0]["id"] != "tc-1" {
		t.Fatalf("tool call id: %v", calls[0]["id"])
	}
}

func TestMapReasoningEffort(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"off", ""},
		{"", ""},
		{"minimal", "low"},
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
	}
	for _, tt := range tests {
		got := mapReasoningEffort(tt.in)
		if got != tt.want {
			t.Errorf("mapReasoningEffort(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
