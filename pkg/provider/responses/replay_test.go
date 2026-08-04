package responses

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

func TestBuildRequestBody_DropsForeignResponseMetadata(t *testing.T) {
	msg := core.Message{
		Role: "assistant", Provider: "xai", Model: "grok-4.5",
		Content: []core.Content{
			{Type: "text", Text: "answer", TextSignature: `{"v":1,"id":"msg_x"}`},
			{Type: "thinking", ThinkingSignature: `{"type":"reasoning","encrypted_content":"secret"}`},
			{Type: "tool_call", ToolCallID: "call_x", ToolName: "tool", ToolCallItemID: "fc_x"},
		},
	}
	body, err := BuildRequestBody(core.Request{Model: core.Model{ID: "gpt-5.5"}, Messages: []core.Message{msg}}, Dialect{Provider: "openai", Model: "gpt-5.5"})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "" {
		t.Fatal("empty request")
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(decoded["input"])
	if string(raw) == "" || strings.Contains(string(raw), "encrypted_content") || strings.Contains(string(raw), "fc_x") || strings.Contains(string(raw), "msg_x") {
		t.Fatalf("foreign metadata leaked into replay: %s", raw)
	}
}

func TestBuildRequestBody_DropsLegacyOpaqueMetadata(t *testing.T) {
	msg := core.Message{Role: "assistant", Content: []core.Content{
		{Type: "text", Text: "answer", TextSignature: `{"v":1,"id":"msg_legacy"}`},
		{Type: "thinking", ThinkingSignature: `{"type":"reasoning","encrypted_content":"secret"}`},
		{Type: "tool_call", ToolCallID: "call_kept", ToolName: "tool", ToolCallItemID: "fc_legacy"},
	}}
	body, err := BuildRequestBody(core.Request{Model: core.Model{ID: "grok-4.5"}, Messages: []core.Message{msg}}, Dialect{Provider: "xai", Model: "grok-4.5"})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(body)
	if strings.Contains(raw, "secret") || strings.Contains(raw, "msg_legacy") || strings.Contains(raw, "fc_legacy") {
		t.Fatalf("legacy opaque metadata leaked: %s", raw)
	}
	if !strings.Contains(raw, "call_kept") {
		t.Fatal("portable call_id pairing was lost")
	}
}

func TestBuildRequestBody_PreservesKnownProviderLegacyModelMetadata(t *testing.T) {
	msg := core.Message{Role: "assistant", Provider: "openai", Content: []core.Content{
		{Type: "text", Text: "answer", TextSignature: `{"v":1,"id":"msg_legacy"}`},
		{Type: "thinking", ThinkingSignature: `{"type":"reasoning","encrypted_content":"legacy-reasoning"}`},
		{Type: "tool_call", ToolCallID: "call_legacy", ToolName: "tool", ToolCallItemID: "fc_legacy"},
	}}
	body, err := BuildRequestBody(core.Request{Model: core.Model{ID: "gpt-5.5"}, Messages: []core.Message{msg}}, Dialect{Provider: "openai", Model: "gpt-5.5"})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(body)
	for _, want := range []string{"msg_legacy", "legacy-reasoning", "fc_legacy", "call_legacy"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("known-provider legacy metadata %q was lost: %s", want, raw)
		}
	}
}
