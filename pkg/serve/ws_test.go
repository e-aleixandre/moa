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
	startedAt := time.UnixMilli(1_700_000_000_000)
	ev, ok := wsEventFromBus(bus.SubagentStarted{
		SessionID: "s1", JobID: "sa-1", Task: "do thing", Model: "haiku", Thinking: "high", Async: true,
		StartedAt: startedAt,
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
	want := SubagentStartData{JobID: "sa-1", Task: "do thing", Model: "haiku", Thinking: "high", Async: true, StartedAtMs: 1_700_000_000_000}
	if data != want {
		t.Fatalf("Data = %+v, want %+v", data, want)
	}

	t.Run("zero start time omits timestamp", func(t *testing.T) {
		ev, _ := wsEventFromBus(bus.SubagentStarted{JobID: "sa-2"})
		if data := ev.Data.(SubagentStartData); data.StartedAtMs != 0 {
			t.Fatalf("StartedAtMs = %d, want 0 for zero time", data.StartedAtMs)
		}
	})
}

func TestBuildInitData_SubagentThinking(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr := newTestManager(t, ctx, newMockProvider(delayedResponseHandler(time.Second, "done")))
	sess, err := mgr.CreateSession(CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}

	subagentTool, ok := sess.infra.toolReg.Get("subagent")
	if !ok {
		t.Fatal("subagent tool not registered")
	}
	if _, err := subagentTool.Execute(context.Background(), map[string]any{
		"task": "inspect the contract", "async": true, "thinking": "medium",
	}, nil); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, time.Second, "subagent job creation", func() bool {
		return len(sess.subagents.Snapshot()) == 1
	})

	data := buildInitData(sess, bus.StreamingAggregate{})
	if len(data.Subagents) != 1 {
		t.Fatalf("Subagents = %+v, want one job", data.Subagents)
	}
	if got := data.Subagents[0].Thinking; got != "medium" {
		t.Fatalf("Thinking = %q, want medium", got)
	}
}

func TestWsEventFromBus_SubagentUsage(t *testing.T) {
	t.Run("with usage", func(t *testing.T) {
		ev, ok := wsEventFromBus(bus.SubagentUsage{
			SessionID: "s1", JobID: "sa-1",
			Usage:   &core.Usage{Input: 100, Output: 42},
			CostUSD: 0.0123,
		})
		if !ok {
			t.Fatal("expected ok=true")
		}
		if ev.Type != "subagent_usage" {
			t.Fatalf("Type = %q, want subagent_usage", ev.Type)
		}
		data, ok := ev.Data.(SubagentUsageData)
		if !ok {
			t.Fatalf("Data type = %T, want SubagentUsageData", ev.Data)
		}
		want := SubagentUsageData{JobID: "sa-1", InputTokens: 100, OutputTokens: 42, CostUSD: 0.0123}
		if data != want {
			t.Fatalf("Data = %+v, want %+v", data, want)
		}
	})

	t.Run("nil usage", func(t *testing.T) {
		ev, ok := wsEventFromBus(bus.SubagentUsage{SessionID: "s1", JobID: "sa-2", Usage: nil, CostUSD: 0})
		if !ok {
			t.Fatal("expected ok=true")
		}
		data := ev.Data.(SubagentUsageData)
		if data.InputTokens != 0 || data.OutputTokens != 0 {
			t.Fatalf("expected zero tokens for nil usage, got %+v", data)
		}
	})

	t.Run("is lossy", func(t *testing.T) {
		ev, _ := wsEventFromBus(bus.SubagentUsage{JobID: "sa-1"})
		if !isLossyWsEvent(ev) {
			t.Fatal("subagent_usage should be lossy")
		}
	})
}

func TestWsEventFromBus_MessageEnded_InputIncludesCache(t *testing.T) {
	// The ↑ heartbeat must reflect the input the model actually processed:
	// fresh input PLUS the cached context replayed each step (CacheRead/Write).
	// Usage.Input alone omits cache, which under Anthropic prompt caching is
	// nearly the whole prompt — the count would read far too low.
	ev, ok := wsEventFromBus(bus.MessageEnded{
		Message: core.AgentMessage{Message: core.Message{
			MsgID: "m1",
			Usage: &core.Usage{Input: 500, CacheRead: 12000, CacheWrite: 1500, Output: 320},
		}},
	})
	if !ok || ev.Type != "message_end" {
		t.Fatalf("Type = %q ok=%v, want message_end", ev.Type, ok)
	}
	data, ok := ev.Data.(MessageEndData)
	if !ok {
		t.Fatalf("Data type = %T, want MessageEndData", ev.Data)
	}
	if data.InputTokens != 14000 { // 500 + 12000 + 1500
		t.Fatalf("InputTokens = %d, want 14000 (input+cache_read+cache_write)", data.InputTokens)
	}
	if data.OutputTokens != 320 {
		t.Fatalf("OutputTokens = %d, want 320", data.OutputTokens)
	}
}

func TestWsEventFromBus_CommandQueued(t *testing.T) {
	ev, ok := wsEventFromBus(bus.CommandQueued{SessionID: "s1", ID: "c1", Raw: "/compact"})
	if !ok || ev.Type != "command_queued" {
		t.Fatalf("Type = %q ok=%v, want command_queued", ev.Type, ok)
	}
	data, ok := ev.Data.(CommandQueuedData)
	if !ok {
		t.Fatalf("Data type = %T, want CommandQueuedData", ev.Data)
	}
	if data != (CommandQueuedData{ID: "c1", Raw: "/compact"}) {
		t.Fatalf("Data = %+v", data)
	}
}

func TestWsEventFromBus_CommandDequeued(t *testing.T) {
	ev, ok := wsEventFromBus(bus.CommandDequeued{SessionID: "s1", ID: "c1", Raw: "/compact", Executed: true})
	if !ok || ev.Type != "command_dequeued" {
		t.Fatalf("Type = %q ok=%v, want command_dequeued", ev.Type, ok)
	}
	data, ok := ev.Data.(CommandDequeuedData)
	if !ok {
		t.Fatalf("Data type = %T, want CommandDequeuedData", ev.Data)
	}
	if data != (CommandDequeuedData{ID: "c1", Raw: "/compact", Executed: true}) {
		t.Fatalf("Data = %+v", data)
	}
}

func TestCountImageContent(t *testing.T) {
	got := countImageContent([]core.Content{
		core.TextContent("hi"),
		core.ImageContent("data", "image/png"),
		core.ImageContent("data2", "image/png"),
	})
	if got != 2 {
		t.Fatalf("countImageContent = %d, want 2", got)
	}
}

func TestLimitInitHistoryBoundsPayloadAndInlineAttachments(t *testing.T) {
	largeImage := strings.Repeat("a", historyContentMaxBytes+1)
	messages := make([]core.AgentMessage, initHistoryMaxMessages+10)
	for i := range messages {
		messages[i] = core.WrapMessage(core.Message{Role: "user", Content: []core.Content{core.TextContent(fmt.Sprintf("message %d", i))}})
	}
	messages[len(messages)-1] = core.WrapMessage(core.Message{Role: "user", Content: []core.Content{{Type: "image", Data: largeImage, MimeType: "image/png"}}})

	limited, truncated := limitInitHistory(messages)
	if !truncated || len(limited) != initHistoryMaxMessages {
		t.Fatalf("limited=%d truncated=%v, want %d and true", len(limited), truncated, initHistoryMaxMessages)
	}
	if got := limited[len(limited)-1].Content[0].Data; got != "" {
		t.Fatalf("inline image retained %d bytes", len(got))
	}
}

func TestLimitInitHistoryBoundsLargeText(t *testing.T) {
	message := core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{core.TextContent(strings.Repeat("x", historyContentMaxBytes+1))}})
	limited, truncated := limitInitHistory([]core.AgentMessage{message})
	if truncated {
		t.Fatal("single bounded message should not be marked as omitted history")
	}
	if got := limited[0].Content[0].Text; len(got) <= historyContentMaxBytes || !strings.Contains(got, "historic content truncated") {
		t.Fatalf("large text was not safely truncated: %d bytes", len(got))
	}
}

func TestLimitInitHistoryDropsOversizedToolArguments(t *testing.T) {
	message := core.WrapMessage(core.Message{Role: "assistant", Content: []core.Content{{
		Type: "tool_call", ToolCallID: "tool-1", ToolName: "bash",
		Arguments: map[string]any{"command": strings.Repeat("x", historyContentMaxBytes+1)},
	}}})
	limited, _ := limitInitHistory([]core.AgentMessage{message})
	args := limited[0].Content[0].Arguments
	if args["_truncated"] != true {
		t.Fatalf("oversized args = %#v, want truncation marker", args)
	}
}

func TestWsEventFromBus_BashJobLifecycle(t *testing.T) {
	start, ok := wsEventFromBus(bus.BashJobStarted{SessionID: "s1", JobID: "bash-1", Command: "go test ./...", CWD: "/work"})
	if !ok || start.Type != "bash_job_start" {
		t.Fatalf("start = %+v, ok=%v", start, ok)
	}
	if got := start.Data.(BashJobStartData); got.JobID != "bash-1" || got.Command != "go test ./..." {
		t.Fatalf("start data = %+v", got)
	}
	output, ok := wsEventFromBus(bus.BashJobOutput{SessionID: "s1", JobID: "bash-1", Delta: "ok\n"})
	if !ok || output.Type != "bash_job_output" || !isLossyWsEvent(output) {
		t.Fatalf("output = %+v, ok=%v", output, ok)
	}
	end, ok := wsEventFromBus(bus.BashJobEnded{SessionID: "s1", JobID: "bash-1", Status: "completed", Output: "ok\n"})
	if !ok || end.Type != "bash_job_end" || isLossyWsEvent(end) {
		t.Fatalf("end = %+v, ok=%v", end, ok)
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

	t.Run("missing file degrades StartLine to 1", func(t *testing.T) {
		e := enrichEditToolStart(editStart(map[string]any{
			"path": filepath.Join(dir, "nope.txt"), "oldText": "x", "newText": "y",
		}), dir)
		d := e.Data.(ToolStartData)
		if d.StartLine != 1 {
			t.Errorf("StartLine = %d, want 1 (degraded)", d.StartLine)
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
