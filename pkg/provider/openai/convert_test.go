package openai

import (
	"encoding/json"
	"strings"
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

	body, err := buildRequestBody(req, true)
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

	body, err := buildRequestBody(req, true)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}

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

	body, err := buildRequestBody(req, true)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatal(err)
	}
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
	result := convertMessage(msg, true, "gpt-5.3-codex", 0)
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
	items := convertMessage(msg, true, "gpt-5.3-codex", 0)
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

func TestConvertAssistantMessage_NilArguments(t *testing.T) {
	// nil arguments should serialize as "{}", not "null".
	msg := core.Message{
		Role: "assistant",
		Content: []core.Content{
			core.ToolCallContent("tc-1", "pwd", nil),
		},
	}
	items := convertAssistantMessage(msg, "gpt-5.3-codex", 0)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	args, _ := items[0]["arguments"].(string)
	if args != "{}" {
		t.Fatalf("arguments = %q, want %q", args, "{}")
	}
}

func TestMapReasoningEffort(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"off", ""},
		{"none", ""},
		{"", ""},
		{"minimal", "minimal"},
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"xhigh", "xhigh"},
	}
	for _, tt := range tests {
		got := mapReasoningEffort(tt.in)
		if got != tt.want {
			t.Errorf("mapReasoningEffort(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestConvertUserContent_Document(t *testing.T) {
	parts := convertUserContent([]core.Content{
		core.DocumentContent("ZGF0YQ==", "application/pdf", "report.pdf"),
	}, true)

	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	if parts[0]["type"] != "input_file" {
		t.Errorf("type: got %v", parts[0]["type"])
	}
	if parts[0]["filename"] != "report.pdf" {
		t.Errorf("filename: got %v", parts[0]["filename"])
	}
	want := "data:application/pdf;base64,ZGF0YQ=="
	if parts[0]["file_data"] != want {
		t.Errorf("file_data: got %v, want %v", parts[0]["file_data"], want)
	}
}

// TestConvertUserContent_DocumentDegraded verifies that a persisted document
// block is NOT emitted as input_file when the active provider (e.g. codex
// OAuth) doesn't support documents — it degrades to a visible text note rather
// than being silently dropped or rejected. Guards against a document leaking to
// an unsupported provider after a mid-conversation model switch.
func TestConvertUserContent_DocumentDegraded(t *testing.T) {
	parts := convertUserContent([]core.Content{
		core.DocumentContent("ZGF0YQ==", "application/pdf", "report.pdf"),
	}, false)

	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	if parts[0]["type"] != "input_text" {
		t.Fatalf("expected input_text degrade, got %v", parts[0]["type"])
	}
	text, _ := parts[0]["text"].(string)
	if !strings.Contains(text, "report.pdf") || !strings.Contains(text, "no reenviado") {
		t.Errorf("degraded note missing filename/notice: %q", text)
	}
}

func TestSupportsDocuments(t *testing.T) {
	if !New("key").SupportsDocuments() {
		t.Error("expected SupportsDocuments true for API-key provider")
	}
	if NewOAuth("tok", "acct").SupportsDocuments() {
		t.Error("expected SupportsDocuments false for OAuth provider")
	}
}

// TestConvertAssistantMessage_RoundTripsSignatures verifies the message item id
// and phase and the function_call fc_ item id are replayed on the next request.
func TestConvertAssistantMessage_RoundTripsSignatures(t *testing.T) {
	msg := core.Message{
		Role: "assistant",
		Content: []core.Content{
			{Type: "text", Text: "done", TextSignature: `{"v":1,"id":"msg_42","phase":"final_answer"}`},
			{Type: "tool_call", ToolCallID: "call_1", ToolName: "bash",
				Arguments: map[string]any{"command": "ls"}, ToolCallItemID: "fc_77"},
		},
	}
	items := convertAssistantMessage(msg, "gpt-5.3-codex", 0)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	// message item carries id + phase.
	if items[0]["id"] != "msg_42" {
		t.Errorf("message id = %v, want msg_42", items[0]["id"])
	}
	if items[0]["phase"] != "final_answer" {
		t.Errorf("message phase = %v, want final_answer", items[0]["phase"])
	}
	// function_call carries the fc_ item id (distinct from call_id).
	if items[1]["id"] != "fc_77" {
		t.Errorf("function_call id = %v, want fc_77", items[1]["id"])
	}
	if items[1]["call_id"] != "call_1" {
		t.Errorf("function_call call_id = %v, want call_1", items[1]["call_id"])
	}
}

// TestConvertAssistantMessage_NoSignatureSyntheticID verifies legacy content
// (no signatures) gets a stable synthetic message id (never omitted) while
// phase stays absent and the function_call fc_ id stays omitted.
func TestConvertAssistantMessage_NoSignatureSyntheticID(t *testing.T) {
	msg := core.Message{
		Role: "assistant",
		Content: []core.Content{
			{Type: "text", Text: "hi"},
			{Type: "tool_call", ToolCallID: "call_1", ToolName: "bash",
				Arguments: map[string]any{"command": "ls"}},
		},
	}
	items := convertAssistantMessage(msg, "gpt-5.3-codex", 7)
	if items[0]["id"] != "msg_moa_7" {
		t.Errorf("legacy message item should get a synthetic id, got %v", items[0]["id"])
	}
	if _, ok := items[0]["phase"]; ok {
		t.Error("legacy message item must not carry a phase key")
	}
	if _, ok := items[1]["id"]; ok {
		t.Error("legacy function_call must not carry an fc_ id key")
	}
}

// TestConvertAssistantMessage_ForeignModelIDs verifies that when a history
// message was produced by a different model, the function_call fc_ id is
// omitted (pairing validation), but the message item still gets a synthetic id
// (not pairing-validated) instead of the foreign real id, while
// call_id/name/args/phase are preserved.
func TestConvertAssistantMessage_ForeignModelIDs(t *testing.T) {
	msg := core.Message{
		Role:  "assistant",
		Model: "gpt-5.6-terra",
		Content: []core.Content{
			{Type: "text", Text: "done", TextSignature: `{"v":1,"id":"msg_42","phase":"final_answer"}`},
			{Type: "tool_call", ToolCallID: "call_1", ToolName: "bash",
				Arguments: map[string]any{"command": "ls"}, ToolCallItemID: "fc_77"},
		},
	}
	// Target model differs from msg.Model.
	items := convertAssistantMessage(msg, "gpt-5.3-codex", 3)
	// Foreign real id is replaced by a synthetic one (never the foreign msg_42).
	if items[0]["id"] != "msg_moa_3" {
		t.Errorf("foreign-model message should get a synthetic id, got %v", items[0]["id"])
	}
	// phase is model-agnostic guidance, safe to keep.
	if items[0]["phase"] != "final_answer" {
		t.Errorf("phase should be preserved, got %v", items[0]["phase"])
	}
	if _, ok := items[1]["id"]; ok {
		t.Error("foreign-model function_call must omit fc_ id")
	}
	if items[1]["call_id"] != "call_1" {
		t.Errorf("call_id must be preserved, got %v", items[1]["call_id"])
	}
	// Same-model keeps the ids.
	same := convertAssistantMessage(msg, "gpt-5.6-terra", 0)
	if same[0]["id"] != "msg_42" || same[1]["id"] != "fc_77" {
		t.Errorf("same-model must keep ids: %v / %v", same[0]["id"], same[1]["id"])
	}
}

// TestParseTextSignature_ValidatesPhase verifies a corrupt/foreign phase is
// dropped on parse (only commentary/final_answer echo back).
func TestParseTextSignature_ValidatesPhase(t *testing.T) {
	cases := []struct {
		sig       string
		wantID    string
		wantPhase string
	}{
		{`{"v":1,"id":"m1","phase":"final_answer"}`, "m1", "final_answer"},
		{`{"v":1,"id":"m1","phase":"commentary"}`, "m1", "commentary"},
		{`{"v":1,"id":"m1","phase":"garbage"}`, "m1", ""},
		{`{"v":1,"id":"m1"}`, "m1", ""},
		{`legacy_bare_id`, "legacy_bare_id", ""},
		{``, "", ""},
		{`{not json`, "", ""},
	}
	for _, c := range cases {
		id, phase := parseTextSignature(c.sig)
		if id != c.wantID || phase != c.wantPhase {
			t.Errorf("parseTextSignature(%q) = %q/%q, want %q/%q", c.sig, id, phase, c.wantID, c.wantPhase)
		}
	}
}
