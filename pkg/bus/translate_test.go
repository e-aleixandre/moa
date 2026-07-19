package bus

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tasks"
)

// TestTranslateAgentEvent_ParityWithBridge checks that TranslateAgentEvent
// (the pure function used by both the session Bridge and the subagent event
// sink) produces exactly the same events that bridgeEvent used to publish
// directly, for a representative sample of AgentEvent types.
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
