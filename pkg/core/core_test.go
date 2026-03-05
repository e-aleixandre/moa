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

	r.Register(Tool{Name: "bash", Description: "Execute commands"})
	r.Register(Tool{Name: "read", Description: "Read files"})

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
	r.Register(Tool{Name: "charlie"})
	r.Register(Tool{Name: "alpha"})
	r.Register(Tool{Name: "bravo"})

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
