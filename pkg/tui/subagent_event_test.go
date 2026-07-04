package tui

import (
	"testing"

	"github.com/ealeixandre/moa/pkg/bus"
)

func TestIsStructuralBusEvent_SubagentEvent_Unwraps(t *testing.T) {
	lossyInner := []any{
		bus.TextDelta{},
		bus.ThinkingDelta{},
		bus.ToolExecUpdate{},
		bus.ToolCallDelta{},
	}
	for _, inner := range lossyInner {
		ev := bus.SubagentEvent{SessionID: "s", JobID: "j1", Inner: inner}
		if isStructuralBusEvent(ev) {
			t.Errorf("isStructuralBusEvent(SubagentEvent{Inner: %T}) = true, want false (lossy)", inner)
		}
	}

	structuralInner := []any{
		bus.ToolExecStarted{},
		bus.MessageEnded{},
		bus.AgentStarted{},
	}
	for _, inner := range structuralInner {
		ev := bus.SubagentEvent{SessionID: "s", JobID: "j1", Inner: inner}
		if !isStructuralBusEvent(ev) {
			t.Errorf("isStructuralBusEvent(SubagentEvent{Inner: %T}) = false, want true (structural)", inner)
		}
	}
}
