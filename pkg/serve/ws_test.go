package serve

import (
	"testing"

	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
)

func TestWsEventFromBus_SubagentStarted(t *testing.T) {
	ev, ok := wsEventFromBus(bus.SubagentStarted{
		SessionID: "s1", JobID: "sa-1", Task: "do thing", Model: "haiku", Async: true,
	})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ev.Type != "subagent_start" {
		t.Fatalf("Type = %q, want subagent_start", ev.Type)
	}
	data, ok := ev.Data.(SubagentStartData)
	if !ok {
		t.Fatalf("Data type = %T, want SubagentStartData", ev.Data)
	}
	want := SubagentStartData{JobID: "sa-1", Task: "do thing", Model: "haiku", Async: true}
	if data != want {
		t.Fatalf("Data = %+v, want %+v", data, want)
	}
}

func TestWsEventFromBus_SubagentEnded(t *testing.T) {
	t.Run("with usage", func(t *testing.T) {
		ev, ok := wsEventFromBus(bus.SubagentEnded{
			SessionID: "s1", JobID: "sa-1", Status: "completed",
			Usage:   &core.Usage{Input: 100, Output: 42},
			CostUSD: 0.0123,
		})
		if !ok {
			t.Fatal("expected ok=true")
		}
		if ev.Type != "subagent_end" {
			t.Fatalf("Type = %q, want subagent_end", ev.Type)
		}
		data, ok := ev.Data.(SubagentEndData)
		if !ok {
			t.Fatalf("Data type = %T, want SubagentEndData", ev.Data)
		}
		want := SubagentEndData{JobID: "sa-1", Status: "completed", InputTokens: 100, OutputTokens: 42, CostUSD: 0.0123}
		if data != want {
			t.Fatalf("Data = %+v, want %+v", data, want)
		}
	})

	t.Run("nil usage", func(t *testing.T) {
		ev, ok := wsEventFromBus(bus.SubagentEnded{
			SessionID: "s1", JobID: "sa-2", Status: "failed", Usage: nil, CostUSD: 0,
		})
		if !ok {
			t.Fatal("expected ok=true")
		}
		data := ev.Data.(SubagentEndData)
		if data.InputTokens != 0 || data.OutputTokens != 0 {
			t.Fatalf("expected zero tokens for nil usage, got %+v", data)
		}
	})
}

func TestWsEventFromBus_SubagentEvent_Translatable(t *testing.T) {
	ev, ok := wsEventFromBus(bus.SubagentEvent{
		SessionID: "s1", JobID: "sa-1",
		Inner: bus.TextDelta{SessionID: "s1", RunGen: 1, Delta: "hello"},
	})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if ev.Type != "subagent_event" {
		t.Fatalf("Type = %q, want subagent_event", ev.Type)
	}
	data, ok := ev.Data.(SubagentEventData)
	if !ok {
		t.Fatalf("Data type = %T, want SubagentEventData", ev.Data)
	}
	if data.JobID != "sa-1" {
		t.Fatalf("JobID = %q, want sa-1", data.JobID)
	}
	if data.Event == nil {
		t.Fatal("Event is nil")
	}
	if data.Event.Type != "text_delta" {
		t.Fatalf("inner Type = %q, want text_delta", data.Event.Type)
	}
	innerData, ok := data.Event.Data.(DeltaData)
	if !ok {
		t.Fatalf("inner Data type = %T, want DeltaData", data.Event.Data)
	}
	if innerData.Delta != "hello" {
		t.Fatalf("inner Delta = %q, want hello", innerData.Delta)
	}
}

func TestWsEventFromBus_SubagentEvent_NonTranslatable(t *testing.T) {
	// AgentStarted has no case in wsEventFromBus, so it must not be wrapped
	// as a subagent_event either.
	_, ok := wsEventFromBus(bus.SubagentEvent{
		SessionID: "s1", JobID: "sa-1",
		Inner: bus.AgentStarted{SessionID: "s1", RunGen: 1},
	})
	if ok {
		t.Fatal("expected ok=false for a non-translatable inner event")
	}
}
