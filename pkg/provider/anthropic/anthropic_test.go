package anthropic

import (
	"os"
	"strings"
	"testing"

	"github.com/ealeixandre/go-agent/pkg/core"
)

func TestMapEvents_SimpleText(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/sse/simple_text.txt")
	if err != nil {
		t.Fatal(err)
	}

	events := mapSSEToEvents(t, string(data))

	// Expected: start, text_start, text_delta("Hello"), text_delta(" world"),
	//           text_end, done
	assertEventTypes(t, events, []string{
		core.ProviderEventStart,
		core.ProviderEventTextStart,
		core.ProviderEventTextDelta,
		core.ProviderEventTextDelta,
		core.ProviderEventTextEnd,
		core.ProviderEventDone,
	})

	// Check deltas
	if events[2].Delta != "Hello" {
		t.Errorf("delta 0: got %q", events[2].Delta)
	}
	if events[3].Delta != " world" {
		t.Errorf("delta 1: got %q", events[3].Delta)
	}

	// Check done has final message
	doneEvt := events[len(events)-1]
	if doneEvt.Message == nil {
		t.Fatal("done event should have Message")
	}
	if doneEvt.Message.StopReason != "end_turn" {
		t.Errorf("stop reason: got %q", doneEvt.Message.StopReason)
	}
	if len(doneEvt.Message.Content) != 1 || doneEvt.Message.Content[0].Text != "Hello world" {
		t.Errorf("final text: got %+v", doneEvt.Message.Content)
	}
}

func TestMapEvents_ToolCall(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/sse/tool_call.txt")
	if err != nil {
		t.Fatal(err)
	}

	events := mapSSEToEvents(t, string(data))

	assertEventTypes(t, events, []string{
		core.ProviderEventStart,
		core.ProviderEventTextStart,
		core.ProviderEventTextDelta,
		core.ProviderEventTextEnd,
		core.ProviderEventToolCallStart,
		core.ProviderEventToolCallDelta,
		core.ProviderEventToolCallDelta,
		core.ProviderEventToolCallEnd,
		core.ProviderEventDone,
	})

	doneEvt := events[len(events)-1]
	if doneEvt.Message == nil {
		t.Fatal("done should have Message")
	}
	if len(doneEvt.Message.Content) != 2 {
		t.Fatalf("expected 2 content blocks (text + tool_call), got %d", len(doneEvt.Message.Content))
	}

	tc := doneEvt.Message.Content[1]
	if tc.Type != "tool_call" || tc.ToolName != "read" {
		t.Errorf("tool call: got %+v", tc)
	}
	if tc.Arguments["path"] != "main.go" {
		t.Errorf("tool call args: got %v", tc.Arguments)
	}
	if doneEvt.Message.StopReason != "tool_use" {
		t.Errorf("stop reason: got %q", doneEvt.Message.StopReason)
	}
}

func TestMapEvents_Thinking(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/sse/thinking.txt")
	if err != nil {
		t.Fatal(err)
	}

	events := mapSSEToEvents(t, string(data))

	assertEventTypes(t, events, []string{
		core.ProviderEventStart,
		core.ProviderEventThinkingStart,
		core.ProviderEventThinkingDelta,
		core.ProviderEventThinkingDelta,
		core.ProviderEventThinkingEnd,
		core.ProviderEventTextStart,
		core.ProviderEventTextDelta,
		core.ProviderEventTextEnd,
		core.ProviderEventDone,
	})

	doneEvt := events[len(events)-1]
	if len(doneEvt.Message.Content) != 2 {
		t.Fatalf("expected 2 content blocks (thinking + text), got %d", len(doneEvt.Message.Content))
	}
	if doneEvt.Message.Content[0].Type != "thinking" {
		t.Error("first block should be thinking")
	}
	if doneEvt.Message.Content[1].Type != "text" {
		t.Error("second block should be text")
	}
}

func TestMapEvents_ErrorEvent(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/sse/error_event.txt")
	if err != nil {
		t.Fatal(err)
	}

	events := mapSSEToEvents(t, string(data))

	// Should have: start, then error
	hasError := false
	for _, e := range events {
		if e.Type == core.ProviderEventError {
			hasError = true
			if e.Error == nil {
				t.Fatal("error event should have Error field")
			}
			if !strings.Contains(e.Error.Error(), "overloaded_error") {
				t.Errorf("expected overloaded error, got: %v", e.Error)
			}
		}
	}
	if !hasError {
		types := make([]string, len(events))
		for i, e := range events {
			types[i] = e.Type
		}
		t.Fatalf("expected error event in: %v", types)
	}
}

func TestMapEvents_MalformedJSON(t *testing.T) {
	data, err := os.ReadFile("../../../testdata/sse/malformed_json.txt")
	if err != nil {
		t.Fatal(err)
	}

	events := mapSSEToEvents(t, string(data))

	// Should produce a ProviderEventError instead of silent nil
	if len(events) != 1 {
		types := make([]string, len(events))
		for i, e := range events {
			types[i] = e.Type
		}
		t.Fatalf("expected 1 event (error), got %d: %v", len(events), types)
	}
	if events[0].Type != core.ProviderEventError {
		t.Fatalf("expected error event, got %q", events[0].Type)
	}
	if events[0].Error == nil || !strings.Contains(events[0].Error.Error(), "parse message_start") {
		t.Errorf("expected parse error, got: %v", events[0].Error)
	}
}

// --- helpers ---

func mapSSEToEvents(t *testing.T, sseData string) []core.AssistantEvent {
	t.Helper()
	a := New("test-key")
	state := &streamState{}
	var events []core.AssistantEvent

	err := parseSSEFrames(strings.NewReader(sseData), func(eventType, data string) {
		event := a.mapEvent(eventType, data, state)
		if event != nil {
			events = append(events, *event)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func assertEventTypes(t *testing.T, events []core.AssistantEvent, expected []string) {
	t.Helper()
	if len(events) != len(expected) {
		types := make([]string, len(events))
		for i, e := range events {
			types[i] = e.Type
		}
		t.Fatalf("expected %d events %v, got %d %v", len(expected), expected, len(events), types)
	}
	for i, exp := range expected {
		if events[i].Type != exp {
			t.Errorf("event %d: expected %q, got %q", i, exp, events[i].Type)
		}
	}
}
