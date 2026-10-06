package core

import "testing"

func TestDropUnansweredToolCalls(t *testing.T) {
	call := func(id string) Content { return Content{Type: "tool_call", ToolCallID: id, ToolName: "write"} }
	msgs := []Message{
		{Role: "assistant", Content: []Content{call("a"), call("b")}},
		{Role: "tool_result", ToolCallID: "a"},
		{Role: "user", Content: []Content{TextContent("hi")}},
		{Role: "assistant", Content: []Content{call("c")}},
		{Role: "user"},
		{Role: "tool_result", ToolCallID: "c"}, // not immediately after
	}
	got := DropUnansweredToolCalls(msgs)
	if len(got[0].Content) != 1 || got[0].Content[0].ToolCallID != "a" {
		t.Fatalf("first assistant = %+v", got[0].Content)
	}
	if len(got) != 5 || len(got[3].Content) != 0 {
		t.Fatalf("late-answered call kept: %+v", got)
	}
	for _, m := range got {
		if m.Role == "tool_result" && m.ToolCallID != "a" {
			t.Fatalf("orphan result left: %+v", m)
		}
	}
	if len(msgs[0].Content) != 2 {
		t.Fatal("input mutated")
	}
	clean := append([]Message(nil), msgs[:2]...)
	clean[0].Content = []Content{call("a")}
	if r := DropUnansweredToolCalls(clean); &r[0] != &clean[0] {
		t.Fatal("clean history should be returned as-is")
	}
}

func TestDropUnansweredToolCalls_ParallelPartialWithLateResult(t *testing.T) {
	call := func(id string) Content { return Content{Type: "tool_call", ToolCallID: id} }
	msgs := []Message{
		{Role: "assistant", Content: []Content{call("a"), call("b")}},
		{Role: "tool_result", ToolCallID: "a"},
		{Role: "user"},
		{Role: "tool_result", ToolCallID: "b"},
	}
	got := DropUnansweredToolCalls(msgs)
	if len(got) != 3 || len(got[0].Content) != 1 || got[0].Content[0].ToolCallID != "a" ||
		got[1].ToolCallID != "a" || got[2].Role != "user" {
		t.Fatalf("got %+v", got)
	}
}
