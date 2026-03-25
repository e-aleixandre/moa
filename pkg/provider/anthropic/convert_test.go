package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

func TestBuildRequestBody_Basic(t *testing.T) {
	req := core.Request{
		Model:  core.Model{ID: "claude-sonnet-4-6"},
		System: "You are a helpful assistant.",
		Messages: []core.Message{
			core.NewUserMessage("Hello"),
		},
		Options: core.StreamOptions{},
	}

	data, err := buildRequestBody(req, false)
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}

	if body["model"] != "claude-sonnet-4-6" {
		t.Errorf("model: got %v", body["model"])
	}
	if body["stream"] != true {
		t.Error("expected stream=true")
	}

	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages: expected 1, got %v", body["messages"])
	}

	msg := msgs[0].(map[string]any)
	if msg["role"] != "user" {
		t.Errorf("message role: got %v", msg["role"])
	}
}

func TestBuildRequestBody_WithTools(t *testing.T) {
	req := core.Request{
		Model: core.Model{ID: "claude-sonnet-4-6"},
		Messages: []core.Message{
			core.NewUserMessage("List files"),
		},
		Tools: []core.ToolSpec{
			{
				Name:        "bash",
				Description: "Execute shell commands",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
			},
		},
	}

	data, err := buildRequestBody(req, false)
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}

	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools: expected 1, got %v", body["tools"])
	}

	tool := tools[0].(map[string]any)
	if tool["name"] != "bash" {
		t.Errorf("tool name: got %v", tool["name"])
	}
	if tool["input_schema"] == nil {
		t.Error("expected input_schema")
	}
}

func TestBuildRequestBody_WithThinkingAdaptive46(t *testing.T) {
	req := core.Request{
		Model: core.Model{ID: "claude-sonnet-4-6"},
		Messages: []core.Message{
			core.NewUserMessage("Think hard"),
		},
		Options: core.StreamOptions{
			ThinkingLevel: "medium",
		},
	}

	data, err := buildRequestBody(req, false)
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}

	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatal("expected thinking config")
	}
	if thinking["type"] != "adaptive" {
		t.Errorf("thinking type: got %v", thinking["type"])
	}

	outputConfig, ok := body["output_config"].(map[string]any)
	if !ok {
		t.Fatal("expected output_config with effort")
	}
	if outputConfig["effort"] != "medium" {
		t.Errorf("effort: got %v, want medium", outputConfig["effort"])
	}
}

func TestBuildRequestBody_WithThinkingManualLegacy(t *testing.T) {
	req := core.Request{
		Model: core.Model{ID: "claude-sonnet-4-5"},
		Messages: []core.Message{
			core.NewUserMessage("Think hard"),
		},
		Options: core.StreamOptions{
			ThinkingLevel: "high",
		},
	}

	data, err := buildRequestBody(req, false)
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}

	thinking, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatal("expected thinking config")
	}
	if thinking["type"] != "enabled" {
		t.Errorf("thinking type: got %v", thinking["type"])
	}

	budget := int(thinking["budget_tokens"].(float64))
	maxTokens := int(body["max_tokens"].(float64))
	if maxTokens <= budget {
		t.Fatalf("max_tokens should be > budget_tokens, got max=%d budget=%d", maxTokens, budget)
	}
}

func TestConvertMessages_ToolResult(t *testing.T) {
	msgs := []core.Message{
		core.NewUserMessage("Read the file"),
		{
			Role: "assistant",
			Content: []core.Content{
				core.TextContent("I'll read it."),
				core.ToolCallContent("toolu_01", "read", map[string]any{"path": "main.go"}),
			},
		},
		core.NewToolResultMessage("toolu_01", "read", []core.Content{core.TextContent("file contents")}, false),
	}

	result := convertMessages(msgs, false)

	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}

	// Third message (tool_result) should be role:"user" with tool_result content block
	toolResultMsg := result[2]
	if toolResultMsg["role"] != "user" {
		t.Errorf("tool result should have role=user, got %v", toolResultMsg["role"])
	}

	content, ok := toolResultMsg["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("expected 1 content block, got %v", toolResultMsg["content"])
	}

	block := content[0].(map[string]any)
	if block["type"] != "tool_result" {
		t.Errorf("expected type=tool_result, got %v", block["type"])
	}
	if block["tool_use_id"] != "toolu_01" {
		t.Errorf("expected tool_use_id=toolu_01, got %v", block["tool_use_id"])
	}
}

func TestConvertToolSpecs_BadJSON(t *testing.T) {
	specs := []core.ToolSpec{
		{
			Name:        "broken",
			Description: "Tool with bad JSON schema",
			Parameters:  json.RawMessage("not json"),
		},
	}
	result := convertToolSpecs(specs, false)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	schema, ok := result[0]["input_schema"].(map[string]any)
	if !ok {
		t.Fatal("expected input_schema to be present as fallback")
	}
	if schema["type"] != "object" {
		t.Errorf("expected fallback {type:object}, got %v", schema)
	}
}

func TestConvertToolSpecs_NonObjectSchema(t *testing.T) {
	specs := []core.ToolSpec{
		{
			Name:        "string_schema",
			Description: "Tool with non-object schema",
			Parameters:  json.RawMessage(`"just a string"`),
		},
	}
	result := convertToolSpecs(specs, false)
	schema, ok := result[0]["input_schema"].(map[string]any)
	if !ok {
		t.Fatal("expected input_schema to be object fallback")
	}
	if schema["type"] != "object" {
		t.Errorf("expected fallback {type:object}, got %v", schema)
	}
}

func TestConvertToolSpecs_ValidSchema(t *testing.T) {
	specs := []core.ToolSpec{
		{
			Name:        "valid",
			Description: "Tool with valid schema",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`),
		},
	}
	result := convertToolSpecs(specs, false)
	schema, ok := result[0]["input_schema"].(map[string]any)
	if !ok {
		t.Fatal("expected input_schema to be preserved")
	}
	if schema["type"] != "object" {
		t.Errorf("expected type=object, got %v", schema)
	}
	if schema["properties"] == nil {
		t.Error("expected properties to be preserved")
	}
}

func TestBuildRequestBody_OAuth_SystemPreamble(t *testing.T) {
	req := core.Request{
		Model:  core.Model{ID: "claude-sonnet-4-6"},
		System: "You are a test agent.",
		Messages: []core.Message{
			core.NewUserMessage("Hello"),
		},
	}

	data, err := buildRequestBody(req, true)
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}

	system, ok := body["system"].([]any)
	if !ok {
		t.Fatal("expected system to be array")
	}
	if len(system) != 2 {
		t.Fatalf("expected 2 system blocks (preamble + custom), got %d", len(system))
	}

	first := system[0].(map[string]any)
	if first["text"] != claudeCodeSystemPreamble {
		t.Errorf("first system block should be CC preamble, got %q", first["text"])
	}
	second := system[1].(map[string]any)
	if second["text"] != "You are a test agent." {
		t.Errorf("second system block should be custom prompt, got %q", second["text"])
	}
}

func TestConvertToolSpecs_OAuth_NameMapping(t *testing.T) {
	specs := []core.ToolSpec{
		{Name: "bash", Description: "Execute commands", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "read", Description: "Read files", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "my_custom_tool", Description: "Custom", Parameters: json.RawMessage(`{"type":"object"}`)},
	}

	result := convertToolSpecs(specs, true)

	if result[0]["name"] != "Bash" {
		t.Errorf("expected 'Bash', got %q", result[0]["name"])
	}
	if result[1]["name"] != "Read" {
		t.Errorf("expected 'Read', got %q", result[1]["name"])
	}
	// Custom tools not in CC list should be unchanged
	if result[2]["name"] != "my_custom_tool" {
		t.Errorf("expected 'my_custom_tool', got %q", result[2]["name"])
	}
}

func TestToolNameRoundTrip(t *testing.T) {
	specs := []core.ToolSpec{
		{Name: "bash"},
		{Name: "edit"},
		{Name: "grep"},
	}

	// To CC casing
	ccBash := toClaudeCodeName("bash")
	if ccBash != "Bash" {
		t.Errorf("expected 'Bash', got %q", ccBash)
	}

	// Back from CC casing
	origBash := fromClaudeCodeName("Bash", specs)
	if origBash != "bash" {
		t.Errorf("expected 'bash', got %q", origBash)
	}

	// Unknown tool stays as-is
	unknown := fromClaudeCodeName("UnknownTool", specs)
	if unknown != "UnknownTool" {
		t.Errorf("expected 'UnknownTool', got %q", unknown)
	}
}

func TestConvertAssistantContent_NilArguments(t *testing.T) {
	// Anthropic requires "input" to be a dict, never null.
	blocks := []core.Content{
		core.ToolCallContent("tc-1", "pwd", nil),
	}
	result := convertAssistantContent(blocks, false)
	if len(result) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result))
	}
	input := result[0].(map[string]any)["input"]
	if input == nil {
		t.Fatal("input should not be nil")
	}
	m, ok := input.(map[string]any)
	if !ok {
		t.Fatalf("input should be map[string]any, got %T", input)
	}
	if len(m) != 0 {
		t.Fatalf("input should be empty map, got %v", m)
	}
}

func TestCacheBreakpoints_SystemAndTools(t *testing.T) {
	req := core.Request{
		Model:  core.Model{ID: "claude-sonnet-4-6"},
		System: "You are helpful.",
		Messages: []core.Message{
			core.NewUserMessage("Hello"),
		},
		Tools: []core.ToolSpec{
			{Name: "bash", Description: "Run commands", Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: "read", Description: "Read files", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
	}

	data, err := buildRequestBody(req, false)
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}

	// Last system block should have cache_control
	system := body["system"].([]any)
	lastSys := system[len(system)-1].(map[string]any)
	cc, ok := lastSys["cache_control"].(map[string]any)
	if !ok {
		t.Fatal("last system block should have cache_control")
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("cache_control type: got %v", cc["type"])
	}

	// Last tool should have cache_control
	tools := body["tools"].([]any)
	lastTool := tools[len(tools)-1].(map[string]any)
	cc, ok = lastTool["cache_control"].(map[string]any)
	if !ok {
		t.Fatal("last tool should have cache_control")
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("cache_control type: got %v", cc["type"])
	}

	// First tool should NOT have cache_control
	firstTool := tools[0].(map[string]any)
	if firstTool["cache_control"] != nil {
		t.Error("first tool should not have cache_control")
	}
}

func TestCacheBreakpoints_LastUserMessage(t *testing.T) {
	req := core.Request{
		Model: core.Model{ID: "claude-sonnet-4-6"},
		Messages: []core.Message{
			core.NewUserMessage("First"),
			{Role: "assistant", Content: []core.Content{core.TextContent("Reply")}},
			core.NewUserMessage("Second"),
		},
	}

	data, err := buildRequestBody(req, false)
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}

	// Last message should be the second user message with cache_control
	msgs := body["messages"].([]any)
	lastMsg := msgs[len(msgs)-1].(map[string]any)
	if lastMsg["role"] != "user" {
		t.Fatalf("last message role: got %v", lastMsg["role"])
	}
	content := lastMsg["content"].([]any)
	lastBlock := content[len(content)-1].(map[string]any)
	cc, ok := lastBlock["cache_control"].(map[string]any)
	if !ok {
		t.Fatal("last user message content block should have cache_control")
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("cache_control type: got %v", cc["type"])
	}

	// First user message should NOT have cache_control
	firstMsg := msgs[0].(map[string]any)
	firstContent := firstMsg["content"].([]any)
	firstBlock := firstContent[0].(map[string]any)
	if firstBlock["cache_control"] != nil {
		t.Error("first user message should not have cache_control")
	}
}

func TestCacheBreakpoints_NoToolsNoPanic(t *testing.T) {
	req := core.Request{
		Model:    core.Model{ID: "claude-sonnet-4-6"},
		Messages: []core.Message{core.NewUserMessage("Hi")},
	}

	// Should not panic with no system, no tools
	data, err := buildRequestBody(req, false)
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}

	// Just verify it doesn't panic and produces valid JSON
	if body["model"] != "claude-sonnet-4-6" {
		t.Errorf("model: got %v", body["model"])
	}
}

func TestConvertMessages_MergeConsecutive(t *testing.T) {
	// Two consecutive tool_results should merge into one user message
	msgs := []core.Message{
		core.NewUserMessage("Do things"),
		{
			Role: "assistant",
			Content: []core.Content{
				core.ToolCallContent("t1", "bash", map[string]any{"command": "ls"}),
				core.ToolCallContent("t2", "bash", map[string]any{"command": "pwd"}),
			},
		},
		core.NewToolResultMessage("t1", "bash", []core.Content{core.TextContent("file1")}, false),
		core.NewToolResultMessage("t2", "bash", []core.Content{core.TextContent("/home")}, false),
	}

	result := convertMessages(msgs, false)

	// user, assistant, user (merged from 2 tool_results)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages (merged tool results), got %d", len(result))
	}

	lastContent, _ := result[2]["content"].([]any)
	if len(lastContent) != 2 {
		t.Fatalf("expected 2 content blocks in merged message, got %d", len(lastContent))
	}
}

func TestConvertAssistantContent_ThinkingWithoutSignature(t *testing.T) {
	// Aborted stream: thinking block without signature should become text
	blocks := []core.Content{
		{Type: "thinking", Thinking: "Let me think about this..."},
		core.TextContent("partial response"),
	}

	result := convertAssistantContent(blocks, false)

	if len(result) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(result))
	}

	// Thinking without signature → text
	first := result[0].(map[string]any)
	if first["type"] != "text" {
		t.Errorf("expected type=text for signatureless thinking, got %v", first["type"])
	}
	if first["text"] != "Let me think about this..." {
		t.Errorf("text content: got %v", first["text"])
	}
	// Must not have thinking/signature fields
	if _, ok := first["thinking"]; ok {
		t.Error("text block should not have thinking field")
	}
	if _, ok := first["signature"]; ok {
		t.Error("text block should not have signature field")
	}
}

func TestConvertAssistantContent_ThinkingWithSignature(t *testing.T) {
	// Normal case: thinking with signature preserved as-is
	blocks := []core.Content{
		{Type: "thinking", Thinking: "reasoning here", ThinkingSignature: "sig123"},
		core.TextContent("response"),
	}

	result := convertAssistantContent(blocks, false)

	first := result[0].(map[string]any)
	if first["type"] != "thinking" {
		t.Errorf("expected type=thinking, got %v", first["type"])
	}
	if first["signature"] != "sig123" {
		t.Errorf("signature: got %v", first["signature"])
	}
}

func TestConvertAssistantContent_EmptyThinkingSkipped(t *testing.T) {
	// Empty thinking blocks should be skipped entirely
	blocks := []core.Content{
		{Type: "thinking", Thinking: "  "},
		core.TextContent("response"),
	}

	result := convertAssistantContent(blocks, false)

	if len(result) != 1 {
		t.Fatalf("expected 1 block (empty thinking skipped), got %d", len(result))
	}
	first := result[0].(map[string]any)
	if first["type"] != "text" {
		t.Errorf("expected text block, got %v", first["type"])
	}
}

func TestConvertAssistantContent_RedactedThinking(t *testing.T) {
	blocks := []core.Content{
		{Type: "thinking", Redacted: true, ThinkingSignature: "opaque-data"},
	}

	result := convertAssistantContent(blocks, false)

	if len(result) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result))
	}
	first := result[0].(map[string]any)
	if first["type"] != "redacted_thinking" {
		t.Errorf("expected redacted_thinking, got %v", first["type"])
	}
	if first["data"] != "opaque-data" {
		t.Errorf("data: got %v", first["data"])
	}
}
