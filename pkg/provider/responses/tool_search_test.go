package responses

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
)

var toolSearchDialect = Dialect{Provider: "openai", Model: "gpt-6.1-sol", SupportsToolSearch: true}

func toolSearchSpecs() []core.ToolSpec {
	obj := json.RawMessage(`{"type":"object","properties":{"ref":{"type":"string"}}}`)
	return []core.ToolSpec{
		{Name: "bash", Description: "Run", Parameters: obj},
		{Name: "mcp__playwright__browser_click", Description: "Click", Parameters: obj, Deferred: true},
		{Name: "mcp__playwright__browser_navigate", Description: "Navigate", Parameters: obj, Deferred: true},
		{Name: "read", Description: "Read", Parameters: obj},
		{Name: core.ToolSearchName, Description: "Search index", Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)},
	}
}

// searchHistory is a conversation that loaded tool through tool_search and
// then called it, as the agent records it.
func searchHistory(model string, refs ...string) []core.Message {
	search := core.ToolCallContent("call_s", core.ToolSearchName, map[string]any{"query": "click", "limit": float64(1)})
	search.ToolCallItemID = "tsc_1"
	result := []core.Content{core.TextContent("Loaded tools: " + strings.Join(refs, ", "))}
	for _, r := range refs {
		result = append(result, core.ToolReferenceContent(r))
	}
	click := core.ToolCallContent("call_c", "mcp__playwright__browser_click", map[string]any{"ref": "e1"})
	click.ToolCallItemID = "fc_1"
	return []core.Message{
		core.NewUserMessage("click it"),
		{Role: "assistant", Provider: "openai", Model: model, Content: []core.Content{search}},
		{Role: "tool_result", ToolCallID: "call_s", ToolName: core.ToolSearchName, Content: result},
		{Role: "assistant", Provider: "openai", Model: model, Content: []core.Content{click}},
		{Role: "tool_result", ToolCallID: "call_c", ToolName: "mcp__playwright__browser_click", Content: []core.Content{core.TextContent("clicked")}},
	}
}

func toolSearchBody(t *testing.T, msgs []core.Message, dialect Dialect) (tools, input []map[string]any) {
	t.Helper()
	body := decodeBody(t, core.Request{Model: core.Model{ID: "gpt-6.1-sol"}, System: "sys", Messages: msgs, Tools: toolSearchSpecs()}, dialect)
	for _, x := range body["tools"].([]any) {
		tools = append(tools, x.(map[string]any))
	}
	for _, x := range body["input"].([]any) {
		input = append(input, x.(map[string]any))
	}
	return tools, input
}

// Deferred definitions stay out of the tool list; tool_search goes as the
// native client-executed tool, so the model knows to emit tool_search_call.
func TestToolSearchDefersMCPOutOfTheToolList(t *testing.T) {
	tools, _ := toolSearchBody(t, []core.Message{core.NewUserMessage("hi")}, toolSearchDialect)
	var names []string
	for _, tl := range tools {
		if tl["type"] == "tool_search" {
			if tl["execution"] != "client" || tl["description"] != "Search index" || tl["parameters"] == nil {
				t.Fatalf("tool_search = %+v", tl)
			}
			names = append(names, "<tool_search>")
			continue
		}
		if tl["type"] != "function" {
			t.Fatalf("tool %+v", tl)
		}
		names = append(names, tl["name"].(string))
	}
	if got := strings.Join(names, ","); got != "bash,read,<tool_search>" {
		t.Fatalf("tools = %s, want the loaded ones and tool_search, in order", got)
	}
}

// A transport that does not declare tool search gets every definition, and
// tool_search as a plain function, exactly as before.
func TestToolSearchOffSendsTheFullList(t *testing.T) {
	for _, dialect := range []Dialect{{Provider: "openai", Model: "gpt-6.1-sol"}, {Provider: "xai", Model: "grok-4.5"}} {
		tools, input := toolSearchBody(t, searchHistory("gpt-6.1-sol", "mcp__playwright__browser_click"), dialect)
		if len(tools) != 5 {
			t.Fatalf("%s: %d tools, want all 5", dialect.Provider, len(tools))
		}
		for _, tl := range tools {
			if tl["type"] != "function" || tl["defer_loading"] != nil {
				t.Fatalf("%s: tool %+v", dialect.Provider, tl)
			}
		}
		raw, _ := json.Marshal(input)
		if strings.Contains(string(raw), "tool_search_") || strings.Contains(string(raw), "tsc_1") {
			t.Fatalf("%s: native tool search items without the capability: %s", dialect.Provider, raw)
		}
		if input[1]["type"] != "function_call" || input[1]["name"] != core.ToolSearchName {
			t.Fatalf("%s: search call = %+v", dialect.Provider, input[1])
		}
		if out, _ := input[2]["output"].(string); !strings.Contains(out, "Loaded tools: mcp__playwright__browser_click") {
			t.Fatalf("%s: search result = %+v, want its text", dialect.Provider, input[2])
		}
	}
}

// With deferral on, the search replays as the native pair and its result
// carries the definitions it loaded, from this request's deferred set. A
// reference to a tool no longer offered is dropped.
func TestToolSearchHistoryReplaysNativeItems(t *testing.T) {
	_, input := toolSearchBody(t, searchHistory("gpt-6.1-sol", "mcp__playwright__browser_click", "mcp__gone__thing"), toolSearchDialect)
	if len(input) != 5 {
		t.Fatalf("input = %+v", input)
	}
	call := input[1]
	args, _ := call["arguments"].(map[string]any)
	if call["type"] != "tool_search_call" || call["id"] != "tsc_1" || call["call_id"] != "call_s" || call["execution"] != "client" ||
		call["status"] != "completed" || args["query"] != "click" || args["limit"] != float64(1) {
		t.Fatalf("search call = %+v, want the native item with object arguments", call)
	}
	out := input[2]
	if out["type"] != "tool_search_output" || out["call_id"] != "call_s" || out["execution"] != "client" || out["status"] != "completed" {
		t.Fatalf("search output = %+v", out)
	}
	loaded, _ := out["tools"].([]any)
	if len(loaded) != 1 {
		t.Fatalf("loaded = %+v, want only the surviving tool", loaded)
	}
	fn := loaded[0].(map[string]any)
	if fn["type"] != "function" || fn["name"] != "mcp__playwright__browser_click" || fn["defer_loading"] != true || fn["description"] != "Click" || fn["parameters"] == nil {
		t.Fatalf("loaded tool = %+v", fn)
	}
	if input[3]["type"] != "function_call" || input[3]["name"] != "mcp__playwright__browser_click" {
		t.Fatalf("call to the loaded tool = %+v", input[3])
	}
}

// A search no tool survives still answers its call, with nothing loaded:
// a call without its output is rejected on every later request.
func TestToolSearchWithNothingLoadedStillAnswers(t *testing.T) {
	msgs := searchHistory("gpt-6.1-sol")[:3]
	msgs[2].Content = []core.Content{core.TextContent("No tools matched.")}
	_, input := toolSearchBody(t, msgs, toolSearchDialect)
	if input[2]["type"] != "tool_search_output" {
		t.Fatalf("output = %+v", input[2])
	}
	if loaded, ok := input[2]["tools"].([]any); !ok || len(loaded) != 0 {
		t.Fatalf("tools = %#v, want an empty list", input[2]["tools"])
	}
}

// A search another model ran replays without its item id, like any other
// cross-model call.
func TestToolSearchFromAnotherModelOmitsItsID(t *testing.T) {
	_, input := toolSearchBody(t, searchHistory("claude-opus-5-5", "mcp__playwright__browser_click"), toolSearchDialect)
	if _, ok := input[1]["id"]; ok || input[1]["type"] != "tool_search_call" {
		t.Fatalf("search call = %+v", input[1])
	}
}

// Loading a tool appends to the input: the previous request's tools and
// input are a byte-identical prefix of the next one.
func TestToolSearchLeavesThePrefixAlone(t *testing.T) {
	msgs := searchHistory("gpt-6.1-sol", "mcp__playwright__browser_click")
	req := func(n int) map[string]any {
		return decodeBody(t, core.Request{Model: core.Model{ID: "gpt-6.1-sol"}, System: "sys", Messages: msgs[:n], Tools: toolSearchSpecs()}, toolSearchDialect)
	}
	for n := 2; n < len(msgs); n++ {
		before, after := req(n), req(n+1)
		bt, _ := json.Marshal(before["tools"])
		at, _ := json.Marshal(after["tools"])
		if string(bt) != string(at) {
			t.Fatalf("tools changed at %d:\n%s\n%s", n, bt, at)
		}
		bi, ai := before["input"].([]any), after["input"].([]any)
		for i := range bi {
			b, _ := json.Marshal(bi[i])
			a, _ := json.Marshal(ai[i])
			if string(a) != string(b) {
				t.Fatalf("input item %d rewritten after %d messages:\n%s\n%s", i, n, b, a)
			}
		}
	}
}

// The model's tool_search_call becomes a tool_search call the loop runs:
// its arguments arrive as an object, not a JSON string, and only on done.
func TestToolSearchCallStreamsAsAToolCall(t *testing.T) {
	sse := `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"tool_search_call","id":"tsc_9","call_id":"call_9","execution":"client","arguments":{},"status":"in_progress"}}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"tool_search_call","id":"tsc_9","call_id":"call_9","execution":"client","arguments":{"query":"weather","limit":1},"status":"completed"}}

data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-6.1-sol","status":"completed","usage":{"input_tokens":10,"output_tokens":1,"total_tokens":11}}}

`
	ch := make(chan core.AssistantEvent, 16)
	go func() {
		ConsumeStream(context.Background(), strings.NewReader(sse), ch, "openai", "gpt-6.1-sol")
		close(ch)
	}()
	var final *core.Message
	for ev := range ch {
		if ev.Type == core.ProviderEventError {
			t.Fatalf("stream error: %v", ev.Error)
		}
		if ev.Type == core.ProviderEventDone {
			final = ev.Message
		}
	}
	if final == nil || final.StopReason != "tool_use" || len(final.Content) != 1 {
		t.Fatalf("final = %+v", final)
	}
	c := final.Content[0]
	if c.Type != "tool_call" || c.ToolName != core.ToolSearchName || c.ToolCallID != "call_9" || c.ToolCallItemID != "tsc_9" ||
		c.Arguments["query"] != "weather" || c.Arguments["limit"] != float64(1) {
		t.Fatalf("call = %+v", c)
	}
}
