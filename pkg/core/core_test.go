package core

import (
	"encoding/json"
	"testing"
)

func TestContentConstructors(t *testing.T) {
	tc := TextContent("hello")
	if tc.Type != "text" || tc.Text != "hello" {
		t.Fatalf("TextContent: got %+v", tc)
	}

	ic := ImageContent("base64data", "image/png")
	if ic.Type != "image" || ic.Data != "base64data" || ic.MimeType != "image/png" {
		t.Fatalf("ImageContent: got %+v", ic)
	}

	thc := ThinkingContent("reasoning")
	if thc.Type != "thinking" || thc.Thinking != "reasoning" {
		t.Fatalf("ThinkingContent: got %+v", thc)
	}

	tlc := ToolCallContent("id-1", "bash", map[string]any{"command": "ls"})
	if tlc.Type != "tool_call" || tlc.ToolCallID != "id-1" || tlc.ToolName != "bash" {
		t.Fatalf("ToolCallContent: got %+v", tlc)
	}
}

func TestContentJSON(t *testing.T) {
	c := TextContent("hello world")
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}

	var c2 Content
	if err := json.Unmarshal(data, &c2); err != nil {
		t.Fatal(err)
	}
	if c2.Type != "text" || c2.Text != "hello world" {
		t.Fatalf("roundtrip: got %+v", c2)
	}
}

func TestMessageJSON(t *testing.T) {
	m := NewUserMessage("test prompt")
	if m.Role != "user" || len(m.Content) != 1 || m.Content[0].Text != "test prompt" {
		t.Fatalf("NewUserMessage: got %+v", m)
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}

	var m2 Message
	if err := json.Unmarshal(data, &m2); err != nil {
		t.Fatal(err)
	}
	if m2.Role != "user" || m2.Content[0].Text != "test prompt" {
		t.Fatalf("roundtrip: got %+v", m2)
	}
}

func TestAgentMessage_IsLLMMessage(t *testing.T) {
	tests := []struct {
		role string
		want bool
	}{
		{"user", true},
		{"assistant", true},
		{"tool_result", true},
		{"custom", false},
		{"system", false},
		{"", false},
	}
	for _, tt := range tests {
		am := AgentMessage{Message: Message{Role: tt.role}}
		if got := am.IsLLMMessage(); got != tt.want {
			t.Errorf("role=%q: IsLLMMessage()=%v, want %v", tt.role, got, tt.want)
		}
	}
}

func TestToolResultMessage(t *testing.T) {
	m := NewToolResultMessage("call-1", "bash", []Content{TextContent("output")}, false)
	if m.Role != "tool_result" || m.ToolCallID != "call-1" || m.ToolName != "bash" || m.IsError {
		t.Fatalf("got %+v", m)
	}

	errm := NewToolResultMessage("call-2", "read", []Content{TextContent("not found")}, true)
	if !errm.IsError {
		t.Fatal("expected IsError=true")
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()

	if r.Count() != 0 {
		t.Fatal("expected empty registry")
	}

	_ = r.Register(Tool{Name: "bash", Description: "Execute commands"})
	_ = r.Register(Tool{Name: "read", Description: "Read files"})

	if r.Count() != 2 {
		t.Fatalf("expected 2 tools, got %d", r.Count())
	}

	bash, ok := r.Get("bash")
	if !ok || bash.Name != "bash" {
		t.Fatal("expected to find bash")
	}

	_, ok = r.Get("nonexistent")
	if ok {
		t.Fatal("expected not found")
	}

	all := r.All()
	if len(all) != 2 {
		t.Fatalf("All: expected 2, got %d", len(all))
	}

	specs := r.Specs()
	if len(specs) != 2 {
		t.Fatalf("Specs: expected 2, got %d", len(specs))
	}

	r.Unregister("bash")
	if r.Count() != 1 {
		t.Fatal("expected 1 after unregister")
	}
}

func TestRegistry_DeterministicOrder(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(Tool{Name: "charlie"})
	_ = r.Register(Tool{Name: "alpha"})
	_ = r.Register(Tool{Name: "bravo"})

	// All() should be sorted
	for i := 0; i < 10; i++ {
		all := r.All()
		if len(all) != 3 {
			t.Fatalf("expected 3, got %d", len(all))
		}
		if all[0].Name != "alpha" || all[1].Name != "bravo" || all[2].Name != "charlie" {
			t.Fatalf("order: %s, %s, %s", all[0].Name, all[1].Name, all[2].Name)
		}
	}

	// Specs() should be sorted
	for i := 0; i < 10; i++ {
		specs := r.Specs()
		if specs[0].Name != "alpha" || specs[1].Name != "bravo" || specs[2].Name != "charlie" {
			t.Fatalf("specs order: %s, %s, %s", specs[0].Name, specs[1].Name, specs[2].Name)
		}
	}
}

func TestToolSpec(t *testing.T) {
	tool := Tool{
		Name:        "bash",
		Description: "Execute shell commands",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
	}
	spec := tool.Spec()
	if spec.Name != "bash" || spec.Description != tool.Description {
		t.Fatalf("Spec: got %+v", spec)
	}
}

func TestResultConstructors(t *testing.T) {
	r := TextResult("hello")
	if len(r.Content) != 1 || r.Content[0].Type != "text" || r.Content[0].Text != "hello" {
		t.Fatalf("TextResult: got %+v", r)
	}
	if r.IsError {
		t.Fatal("TextResult should not be IsError")
	}

	e := ErrorResult("boom")
	if len(e.Content) != 1 || e.Content[0].Text != "Error: boom" {
		t.Fatalf("ErrorResult: got %+v", e)
	}
	if !e.IsError {
		t.Fatal("ErrorResult should have IsError=true")
	}
}

func TestEstimateTokens_Text(t *testing.T) {
	m := NewUserMessage("hello world") // 11 chars → ceil(11/4) = 3
	got := EstimateTokens(m)
	if got != 3 {
		t.Fatalf("expected 3, got %d", got)
	}
}

func TestEstimateTokens_ToolCall(t *testing.T) {
	m := Message{
		Role: "assistant",
		Content: []Content{
			ToolCallContent("id-1", "bash", map[string]any{"command": "ls -la"}),
		},
	}
	got := EstimateTokens(m)
	// "bash" (4) + JSON of {"command":"ls -la"} (~20 chars) → ~24 chars → 6 tokens
	if got < 5 || got > 10 {
		t.Fatalf("expected 5-10, got %d", got)
	}
}

func TestEstimateTokens_Image(t *testing.T) {
	m := Message{
		Role:    "user",
		Content: []Content{ImageContent("base64...", "image/png")},
	}
	got := EstimateTokens(m)
	if got != 1200 { // 4800/4
		t.Fatalf("expected 1200, got %d", got)
	}
}

func TestEstimateTokens_Empty(t *testing.T) {
	m := Message{Role: "user"}
	got := EstimateTokens(m)
	if got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}

func TestEstimateTokens_ToolResult(t *testing.T) {
	m := NewToolResultMessage("call-1", "bash", []Content{TextContent("output text")}, false)
	got := EstimateTokens(m)
	// "output text" (11) + "bash" (4) + "call-1" (6) = 21 chars → ceil(21/4) = 6
	want := (11 + 4 + 6 + 3) / 4
	if got != want {
		t.Fatalf("expected %d, got %d", want, got)
	}
}

func TestEstimateContextTokens_NoUsage(t *testing.T) {
	msgs := []AgentMessage{
		WrapMessage(NewUserMessage("hello world")),          // 11 chars → 3
		WrapMessage(Message{Role: "assistant", Content: []Content{TextContent("hi there!")}}), // 9 chars → 3
	}
	est := EstimateContextTokens(msgs, "system prompt here", nil, 0)
	// Messages: 3 + 3 = 6. System prompt: "system prompt here" (18 chars) → 5.
	if est.Tokens != est.TrailingTokens+est.OverheadTokens {
		t.Fatalf("tokens mismatch: total=%d, trailing=%d, overhead=%d", est.Tokens, est.TrailingTokens, est.OverheadTokens)
	}
	if est.UsageTokens != 0 {
		t.Fatalf("expected UsageTokens=0, got %d", est.UsageTokens)
	}
	if est.OverheadTokens == 0 {
		t.Fatal("expected non-zero overhead")
	}
}

func TestEstimateContextTokens_WithUsage(t *testing.T) {
	msgs := []AgentMessage{
		WrapMessage(NewUserMessage("first")),
		{
			Message: Message{
				Role:    "assistant",
				Content: []Content{TextContent("response")},
				Usage:   &Usage{Input: 100, Output: 50},
			},
			Custom: map[string]any{"compaction_epoch": 0},
		},
		WrapMessage(NewUserMessage("second")), // 6 chars → 2
	}
	est := EstimateContextTokens(msgs, "sys", nil, 0)
	if est.UsageTokens != 150 { // Input(100) + Output(50)
		t.Fatalf("expected UsageTokens=150, got %d", est.UsageTokens)
	}
	if est.TrailingTokens != 2 { // "second" = 6 chars → 2
		t.Fatalf("expected TrailingTokens=2, got %d", est.TrailingTokens)
	}
	if est.Tokens != 152 { // 150 + 2
		t.Fatalf("expected Tokens=152, got %d", est.Tokens)
	}
}

func TestEstimateContextTokens_StaleUsage(t *testing.T) {
	msgs := []AgentMessage{
		WrapMessage(NewUserMessage("first")),
		{
			Message: Message{
				Role:    "assistant",
				Content: []Content{TextContent("old response")},
				Usage:   &Usage{TotalTokens: 99999}, // stale
			},
			Custom: map[string]any{"compaction_epoch": 0},
		},
		WrapMessage(NewUserMessage("second")),
	}
	// Epoch 1 doesn't match the assistant's epoch 0 → usage ignored.
	est := EstimateContextTokens(msgs, "", nil, 1)
	if est.UsageTokens != 0 {
		t.Fatalf("expected stale usage to be ignored, got UsageTokens=%d", est.UsageTokens)
	}
	if est.TrailingTokens == 0 {
		t.Fatal("expected non-zero trailing (all estimated)")
	}
}

func TestEstimateContextTokens_ToolSpecOverhead(t *testing.T) {
	specs := []ToolSpec{
		{Name: "bash", Description: "Execute commands", Parameters: json.RawMessage(`{"type":"object"}`)},
	}
	est := EstimateContextTokens(nil, "system", specs, 0)
	if est.OverheadTokens == 0 {
		t.Fatal("expected overhead from system prompt + tool specs")
	}
	// Just system: "system" (6 chars) → 2.
	// Tool: "bash" (4) + "Execute commands" (16) + `{"type":"object"}` (17) → 37 chars → 10.
	// Total overhead ≈ 12.
	if est.OverheadTokens < 10 {
		t.Fatalf("expected overhead >= 10, got %d", est.OverheadTokens)
	}
}

func TestShouldCompact(t *testing.T) {
	s := CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecent: 20000}

	// Below threshold → false
	if ShouldCompact(100_000, 200_000, s) {
		t.Fatal("100K < 200K-16K, should not compact")
	}
	// Above threshold → true
	if !ShouldCompact(190_000, 200_000, s) {
		t.Fatal("190K > 200K-16K, should compact")
	}
	// Exactly at threshold → false
	if ShouldCompact(183_616, 200_000, s) {
		t.Fatal("at threshold, should not compact")
	}
	// Just above → true
	if !ShouldCompact(183_617, 200_000, s) {
		t.Fatal("just above threshold, should compact")
	}
}

func TestShouldCompact_Disabled(t *testing.T) {
	s := CompactionSettings{Enabled: false, ReserveTokens: 16384}
	if ShouldCompact(999_999, 200_000, s) {
		t.Fatal("disabled should always return false")
	}
}

func TestShouldCompact_ZeroWindow(t *testing.T) {
	s := CompactionSettings{Enabled: true, ReserveTokens: 16384}
	if ShouldCompact(100_000, 0, s) {
		t.Fatal("zero window should return false")
	}
}

func TestShouldCompact_ReserveExceedsWindow(t *testing.T) {
	s := CompactionSettings{Enabled: true, ReserveTokens: 300_000}
	if ShouldCompact(100_000, 200_000, s) {
		t.Fatal("reserve >= window should return false")
	}
}

func TestAssistantEvent_IsTerminal(t *testing.T) {
	tests := []struct {
		typ  string
		want bool
	}{
		{ProviderEventDone, true},
		{ProviderEventError, true},
		{ProviderEventTextDelta, false},
		{ProviderEventStart, false},
	}
	for _, tt := range tests {
		e := AssistantEvent{Type: tt.typ}
		if got := e.IsTerminal(); got != tt.want {
			t.Errorf("type=%q: IsTerminal()=%v, want %v", tt.typ, got, tt.want)
		}
	}
}
