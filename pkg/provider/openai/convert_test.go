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
	if parsed["instructions"] != "You are helpful." {
		t.Fatalf("instructions: %v", parsed["instructions"])
	}

	input, _ := parsed["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("expected 1 input item, got %d", len(input))
	}

	first := input[0].(map[string]any)
	if first["role"] != "user" {
		t.Fatalf("first input role: %v", first["role"])
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
	if tool["name"] != "bash" {
		t.Fatalf("tool name: %v", tool["name"])
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
	r, ok := parsed["reasoning"].(map[string]any)
	if !ok {
		t.Fatal("expected reasoning object")
	}
	if r["effort"] != "high" {
		t.Fatalf("effort: %v", r["effort"])
	}
}

func TestConvertMessage_ToolResult(t *testing.T) {
	msg := core.NewToolResultMessage("call-1", "bash", []core.Content{core.TextContent("output")}, false)
	result := convertMessage(msg)
	if len(result) != 1 {
		t.Fatalf("expected 1 item, got %d", len(result))
	}
	item := result[0]
	if item["type"] != "function_call_output" {
		t.Fatalf("type: %v", item["type"])
	}
	if item["call_id"] != "call-1" {
		t.Fatalf("call_id: %v", item["call_id"])
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
	items := convertMessage(msg)
	// Should produce 2 items: a message item and a function_call item.
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0]["type"] != "message" {
		t.Fatalf("first item type: %v", items[0]["type"])
	}
	if items[1]["type"] != "function_call" {
		t.Fatalf("second item type: %v", items[1]["type"])
	}
	if items[1]["call_id"] != "tc-1" {
		t.Fatalf("call_id: %v", items[1]["call_id"])
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
