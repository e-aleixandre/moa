package serve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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

func TestIsLossyWsEvent(t *testing.T) {
	cases := []struct {
		name  string
		event Event
		want  bool
	}{
		{"text_delta", Event{Type: "text_delta"}, true},
		{"thinking_delta", Event{Type: "thinking_delta"}, true},
		{"tool_update", Event{Type: "tool_update"}, true},
		{"tool_call_delta", Event{Type: "tool_call_delta"}, true},
		{"message_end structural", Event{Type: "message_end"}, false},
		{"tool_end structural", Event{Type: "tool_end"}, false},
		{"run_end structural", Event{Type: "run_end"}, false},
		{"subagent_start structural", Event{Type: "subagent_start"}, false},
		{"subagent_end structural", Event{Type: "subagent_end"}, false},
		{
			"subagent_event wrapping text_delta is lossy",
			Event{Type: "subagent_event", Data: SubagentEventData{
				JobID: "sa-1", Event: &Event{Type: "text_delta"},
			}},
			true,
		},
		{
			"subagent_event wrapping message_end is structural",
			Event{Type: "subagent_event", Data: SubagentEventData{
				JobID: "sa-1", Event: &Event{Type: "message_end"},
			}},
			false,
		},
		{
			"subagent_event with nil inner is structural",
			Event{Type: "subagent_event", Data: SubagentEventData{JobID: "sa-1"}},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLossyWsEvent(tc.event); got != tc.want {
				t.Fatalf("isLossyWsEvent(%q) = %v, want %v", tc.event.Type, got, tc.want)
			}
		})
	}
}

// TestWSReactor_CleanupStopsWatcher verifies the context-watcher goroutine exits
// when the reactor is cleaned up early (e.g. a WS reconnect) even though the
// session context is still alive — otherwise each reconnect leaks a goroutine
// plus its 512-slot channel until the whole session ends.
func TestWSReactor_CleanupStopsWatcher(t *testing.T) {
	b := bus.NewLocalBus()
	defer b.Close()

	ctx := context.Background() // never cancelled: the watcher must exit via r.done
	runtime.GC()
	before := runtime.NumGoroutine()

	r := newWsReactor(b, ctx, "")
	r.cleanup()

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("watcher goroutine leaked after cleanup: before=%d now=%d", before, runtime.NumGoroutine())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEnrichEditToolStart(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	editStart := func(args map[string]any) Event {
		return Event{Type: "tool_start", Data: ToolStartData{
			ToolCallID: "tc1", ToolName: "edit", Args: args,
		}}
	}

	t.Run("edit at line 260 gets start_line 260", func(t *testing.T) {
		e := enrichEditToolStart(editStart(map[string]any{
			"path": path, "oldText": "line 260\nline 261", "newText": "x",
		}), dir)
		d := e.Data.(ToolStartData)
		if d.StartLine != 260 {
			t.Errorf("StartLine = %d, want 260", d.StartLine)
		}
	})

	t.Run("relative path resolves against cwd", func(t *testing.T) {
		e := enrichEditToolStart(editStart(map[string]any{
			"path": "big.txt", "oldText": "line 42", "newText": "x",
		}), dir)
		d := e.Data.(ToolStartData)
		if d.StartLine != 42 {
			t.Errorf("StartLine = %d, want 42", d.StartLine)
		}
	})

	t.Run("oldText not found degrades to 1", func(t *testing.T) {
		e := enrichEditToolStart(editStart(map[string]any{
			"path": path, "oldText": "no such content here", "newText": "x",
		}), dir)
		d := e.Data.(ToolStartData)
		if d.StartLine != 1 {
			t.Errorf("StartLine = %d, want 1", d.StartLine)
		}
	})

	t.Run("missing file leaves StartLine 0", func(t *testing.T) {
		e := enrichEditToolStart(editStart(map[string]any{
			"path": filepath.Join(dir, "nope.txt"), "oldText": "x", "newText": "y",
		}), dir)
		d := e.Data.(ToolStartData)
		if d.StartLine != 0 {
			t.Errorf("StartLine = %d, want 0", d.StartLine)
		}
	})

	t.Run("non-edit tools untouched", func(t *testing.T) {
		e := enrichEditToolStart(Event{Type: "tool_start", Data: ToolStartData{
			ToolCallID: "tc1", ToolName: "bash", Args: map[string]any{"command": "ls"},
		}}, dir)
		d := e.Data.(ToolStartData)
		if d.StartLine != 0 {
			t.Errorf("StartLine = %d, want 0", d.StartLine)
		}
	})

	t.Run("non-tool_start events untouched", func(t *testing.T) {
		orig := Event{Type: "text_delta", Data: DeltaData{Delta: "hi"}}
		if got := enrichEditToolStart(orig, dir); got != orig {
			t.Errorf("event was modified: %+v", got)
		}
	})
}
