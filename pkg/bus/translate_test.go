package bus

import (
	"errors"
	"reflect"
	"testing"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

// TestTranslateAgentEvent_ParityWithBridge checks that TranslateAgentEvent
// (the pure function used by both the session Bridge and the subagent event
// sink) produces exactly the events bridgeEvent publishes, for a
// representative sample of AgentEvent types.
func TestTranslateAgentEvent_ParityWithBridge(t *testing.T) {
	const sid = "sess-1"
	const gen = uint64(7)

	cases := []struct {
		name string
		in   core.AgentEvent
		want []any
	}{
		{
			name: "start",
			in:   core.AgentEvent{Type: core.AgentEventStart},
			want: []any{AgentStarted{SessionID: sid, RunGen: gen}},
		},
		{
			name: "end",
			in:   core.AgentEvent{Type: core.AgentEventEnd, Messages: []core.AgentMessage{{Message: core.Message{Role: "assistant"}}}},
			want: []any{AgentEnded{SessionID: sid, RunGen: gen, Messages: []core.AgentMessage{{Message: core.Message{Role: "assistant"}}}}},
		},
		{
			name: "error",
			in:   core.AgentEvent{Type: core.AgentEventError, Error: errors.New("boom")},
			want: []any{AgentError{SessionID: sid, RunGen: gen, Err: errors.New("boom")}},
		},
		{
			name: "text_delta",
			in: core.AgentEvent{
				Type:           core.AgentEventMessageUpdate,
				AssistantEvent: &core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: "hi"},
			},
			want: []any{TextDelta{SessionID: sid, RunGen: gen, Delta: "hi"}},
		},
		{
			name: "message_update_nil_assistant_event",
			in:   core.AgentEvent{Type: core.AgentEventMessageUpdate},
			want: nil,
		},
		{
			name: "message_end",
			in: core.AgentEvent{
				Type: core.AgentEventMessageEnd,
				Message: core.AgentMessage{Message: core.Message{
					Role:    "assistant",
					Content: []core.Content{{Type: "text", Text: "hello"}},
				}},
			},
			want: []any{MessageEnded{
				SessionID: sid, RunGen: gen,
				Message: core.AgentMessage{Message: core.Message{
					Role:    "assistant",
					Content: []core.Content{{Type: "text", Text: "hello"}},
				}},
				FullText: "hello",
			}},
		},
		{
			name: "tool_exec_start",
			in: core.AgentEvent{
				Type:       core.AgentEventToolExecStart,
				ToolCallID: "tc1",
				ToolName:   "bash",
				Args:       map[string]any{"cmd": "ls"},
			},
			want: []any{ToolExecStarted{SessionID: sid, RunGen: gen, ToolCallID: "tc1", ToolName: "bash", Args: map[string]any{"cmd": "ls"}}},
		},
		{
			name: "tool_exec_end_no_tasks",
			in: core.AgentEvent{
				Type:       core.AgentEventToolExecEnd,
				ToolCallID: "tc1",
				ToolName:   "bash",
				Result:     &core.Result{Content: []core.Content{{Type: "text", Text: "ok"}}},
			},
			want: []any{ToolExecEnded{SessionID: sid, RunGen: gen, ToolCallID: "tc1", ToolName: "bash", Result: "ok"}},
		},
		{
			name: "steer",
			in:   core.AgentEvent{Type: core.AgentEventSteer, SteerID: "st1", Text: "steer msg"},
			want: []any{Steered{SessionID: sid, RunGen: gen, ID: "st1", Text: "steer msg"}},
		},
		{
			// A steer with attachments must publish its blocks so clients paint
			// the thumbnails live, not only after a reload.
			name: "steer_with_attachment",
			in: core.AgentEvent{
				Type: core.AgentEventSteer, SteerID: "st2", MsgID: "m2", Text: "mira esto",
				Message: core.WrapMessage(core.NewUserMessageWithContent([]core.Content{
					{Type: "image", Data: "aW1n", MimeType: "image/png"},
					{Type: "text", Text: "mira esto"},
				})),
			},
			want: []any{Steered{
				SessionID: sid, RunGen: gen, ID: "st2", MsgID: "m2", Text: "mira esto",
				Content: []core.Content{
					{Type: "image", Data: "aW1n", MimeType: "image/png"},
					{Type: "text", Text: "mira esto"},
				},
			}},
		},
		{
			// Text-only steers keep their old shape: no redundant blocks on the wire.
			name: "steer_text_only",
			in: core.AgentEvent{
				Type: core.AgentEventSteer, SteerID: "st3", Text: "plain",
				Message: core.WrapMessage(core.NewUserMessage("plain")),
			},
			want: []any{Steered{SessionID: sid, RunGen: gen, ID: "st3", Text: "plain"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TranslateAgentEvent(sid, gen, tc.in, nil)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("TranslateAgentEvent(%s) = %#v, want %#v", tc.name, got, tc.want)
			}
		})
	}
}

// TestTranslateAgentEvent_ToolExecEnd_TasksUpdate verifies the special-cased
// TasksUpdated side event fires only when ToolName=="tasks" and a non-nil
// taskStore is supplied — mirroring the original bridgeEvent behavior.
func TestTranslateAgentEvent_ToolExecEnd_TasksUpdate(t *testing.T) {
	store := tasks.NewStore()

	e := core.AgentEvent{Type: core.AgentEventToolExecEnd, ToolName: "tasks"}

	withStore := TranslateAgentEvent("s", 1, e, store)
	if len(withStore) != 2 {
		t.Fatalf("expected 2 events with taskStore, got %d: %#v", len(withStore), withStore)
	}
	if _, ok := withStore[1].(TasksUpdated); !ok {
		t.Fatalf("expected second event to be TasksUpdated, got %T", withStore[1])
	}

	withoutStore := TranslateAgentEvent("s", 1, e, nil)
	if len(withoutStore) != 1 {
		t.Fatalf("expected 1 event without taskStore, got %d: %#v", len(withoutStore), withoutStore)
	}

	otherTool := TranslateAgentEvent("s", 1, core.AgentEvent{Type: core.AgentEventToolExecEnd, ToolName: "bash"}, store)
	if len(otherTool) != 1 {
		t.Fatalf("expected 1 event for non-tasks tool, got %d: %#v", len(otherTool), otherTool)
	}
}

func TestTranslateAgentEvent_FastUnavailablePublishesExplicitFalse(t *testing.T) {
	events := TranslateAgentEvent("s", 1, core.AgentEvent{Type: core.AgentEventFastUnavailable}, nil)
	if len(events) != 1 {
		t.Fatalf("events = %v, want one ConfigChanged", events)
	}
	changed, ok := events[0].(ConfigChanged)
	if !ok || changed.Fast == nil || *changed.Fast {
		t.Fatalf("event = %#v, want ConfigChanged with Fast false", events[0])
	}
}

func TestIsLossyEvent_SubagentUsage(t *testing.T) {
	if !isLossyEvent(SubagentUsage{SessionID: "s", JobID: "job1"}) {
		t.Error("isLossyEvent(SubagentUsage) = false, want true")
	}
}

func TestIsLossyEvent_SubagentEvent_Unwraps(t *testing.T) {
	lossyCases := []any{
		TextDelta{},
		ThinkingDelta{},
		ToolExecUpdate{},
		ToolCallDelta{},
	}
	for _, inner := range lossyCases {
		ev := SubagentEvent{SessionID: "s", JobID: "job1", Inner: inner}
		if !isLossyEvent(ev) {
			t.Errorf("isLossyEvent(SubagentEvent{Inner: %T}) = false, want true", inner)
		}
	}

	structuralCases := []any{
		ToolExecStarted{},
		MessageEnded{},
		AgentStarted{},
	}
	for _, inner := range structuralCases {
		ev := SubagentEvent{SessionID: "s", JobID: "job1", Inner: inner}
		if isLossyEvent(ev) {
			t.Errorf("isLossyEvent(SubagentEvent{Inner: %T}) = true, want false", inner)
		}
	}
}
