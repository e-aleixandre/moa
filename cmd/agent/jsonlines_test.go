package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/ealeixandre/moa/pkg/core"
)

// captureJSONLines runs fn with stdout redirected and returns parsed JSON objects.
func captureJSONLines(t *testing.T, fn func()) []map[string]any {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	oldStdout := os.Stdout
	os.Stdout = w

	fn()

	os.Stdout = oldStdout
	w.Close()

	var buf bytes.Buffer
	buf.ReadFrom(r)
	r.Close()

	var lines []map[string]any
	dec := json.NewDecoder(&buf)
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("failed to decode JSON line: %v\nraw: %s", err, buf.String())
		}
		lines = append(lines, m)
	}
	return lines
}

func TestJSONLineWriter_AgentStartEnd(t *testing.T) {
	lines := captureJSONLines(t, func() {
		jw := newJSONLineWriter()
		jw.handle(core.AgentEvent{Type: core.AgentEventStart})
		jw.handle(core.AgentEvent{Type: core.AgentEventEnd})
	})

	// agent_start, summary, agent_end
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lines[0]["type"] != "agent_start" {
		t.Errorf("expected agent_start, got %v", lines[0]["type"])
	}
	if lines[1]["type"] != "summary" {
		t.Errorf("expected summary, got %v", lines[1]["type"])
	}
	if lines[2]["type"] != "agent_end" {
		t.Errorf("expected agent_end, got %v", lines[2]["type"])
	}
}

func TestJSONLineWriter_TextDelta(t *testing.T) {
	lines := captureJSONLines(t, func() {
		jw := newJSONLineWriter()
		jw.handle(core.AgentEvent{
			Type: core.AgentEventMessageUpdate,
			AssistantEvent: &core.AssistantEvent{
				Type:  core.ProviderEventTextDelta,
				Delta: "Hello world",
			},
		})
	})

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0]["type"] != "message_update" {
		t.Errorf("expected message_update, got %v", lines[0]["type"])
	}
	if lines[0]["event_type"] != "text_delta" {
		t.Errorf("expected text_delta, got %v", lines[0]["event_type"])
	}
	if lines[0]["delta"] != "Hello world" {
		t.Errorf("expected 'Hello world', got %v", lines[0]["delta"])
	}
}

func TestJSONLineWriter_ThinkingDelta(t *testing.T) {
	lines := captureJSONLines(t, func() {
		jw := newJSONLineWriter()
		jw.handle(core.AgentEvent{
			Type: core.AgentEventMessageUpdate,
			AssistantEvent: &core.AssistantEvent{
				Type:  core.ProviderEventThinkingDelta,
				Delta: "hmm...",
			},
		})
	})

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0]["event_type"] != "thinking_delta" {
		t.Errorf("expected thinking_delta, got %v", lines[0]["event_type"])
	}
}

func TestJSONLineWriter_ToolExecution(t *testing.T) {
	lines := captureJSONLines(t, func() {
		jw := newJSONLineWriter()
		jw.handle(core.AgentEvent{
			Type:       core.AgentEventToolExecStart,
			ToolCallID: "tc_1",
			ToolName:   "bash",
			Args:       map[string]any{"command": "ls -la"},
		})
		jw.handle(core.AgentEvent{
			Type:       core.AgentEventToolExecUpdate,
			ToolCallID: "tc_1",
			Result:     &core.Result{Content: []core.Content{{Type: "text", Text: "file1.txt"}}},
		})
		jw.handle(core.AgentEvent{
			Type:       core.AgentEventToolExecUpdate,
			ToolCallID: "tc_1",
			Result:     &core.Result{Content: []core.Content{{Type: "text", Text: "file1.txt\nfile2.txt"}}},
		})
		jw.handle(core.AgentEvent{
			Type:       core.AgentEventToolExecEnd,
			ToolCallID: "tc_1",
			ToolName:   "bash",
			IsError:    false,
		})
	})

	// start, update1, update2, end, progress
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}

	// Start
	if lines[0]["type"] != "tool_execution_start" {
		t.Errorf("expected tool_execution_start, got %v", lines[0]["type"])
	}
	if lines[0]["tool_name"] != "bash" {
		t.Errorf("expected bash, got %v", lines[0]["tool_name"])
	}
	args := lines[0]["args"].(map[string]any)
	if args["command"] != "ls -la" {
		t.Errorf("expected 'ls -la', got %v", args["command"])
	}

	// Update 1: full text = "file1.txt" → delta = "file1.txt"
	if lines[1]["text"] != "file1.txt" {
		t.Errorf("expected 'file1.txt', got %v", lines[1]["text"])
	}

	// Update 2: full text = "file1.txt\nfile2.txt" → delta = "\nfile2.txt"
	if lines[2]["text"] != "\nfile2.txt" {
		t.Errorf("expected '\\nfile2.txt', got %v", lines[2]["text"])
	}

	// End
	if lines[3]["type"] != "tool_execution_end" {
		t.Errorf("expected tool_execution_end, got %v", lines[3]["type"])
	}
	if lines[3]["is_error"] != false {
		t.Errorf("expected is_error=false, got %v", lines[3]["is_error"])
	}

	// Progress after tool_execution_end
	if lines[4]["type"] != "progress" {
		t.Errorf("expected progress, got %v", lines[4]["type"])
	}
}

func TestJSONLineWriter_ToolExecEnd_Error(t *testing.T) {
	lines := captureJSONLines(t, func() {
		jw := newJSONLineWriter()
		jw.handle(core.AgentEvent{
			Type:       core.AgentEventToolExecEnd,
			ToolCallID: "tc_2",
			ToolName:   "bash",
			IsError:    true,
		})
	})

	// tool_execution_end + progress
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0]["is_error"] != true {
		t.Errorf("expected is_error=true, got %v", lines[0]["is_error"])
	}
	if lines[1]["type"] != "progress" {
		t.Errorf("expected progress, got %v", lines[1]["type"])
	}
}

func TestJSONLineWriter_AgentError(t *testing.T) {
	lines := captureJSONLines(t, func() {
		jw := newJSONLineWriter()
		jw.handle(core.AgentEvent{
			Type:  core.AgentEventError,
			Error: os.ErrDeadlineExceeded,
		})
	})

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0]["type"] != "agent_error" {
		t.Errorf("expected agent_error, got %v", lines[0]["type"])
	}
	if lines[0]["error"] != "i/o timeout" {
		t.Errorf("expected 'i/o timeout', got %v", lines[0]["error"])
	}
}

func TestJSONLineWriter_Compaction(t *testing.T) {
	lines := captureJSONLines(t, func() {
		jw := newJSONLineWriter()
		jw.handle(core.AgentEvent{Type: core.AgentEventCompactionStart})
		jw.handle(core.AgentEvent{Type: core.AgentEventCompactionEnd})
	})

	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0]["type"] != "compaction_start" {
		t.Errorf("expected compaction_start, got %v", lines[0]["type"])
	}
	if lines[1]["type"] != "compaction_end" {
		t.Errorf("expected compaction_end, got %v", lines[1]["type"])
	}
}

func TestJSONLineWriter_IgnoredEvents(t *testing.T) {
	lines := captureJSONLines(t, func() {
		jw := newJSONLineWriter()
		// TurnStart is no longer ignored (increments turn counter), but
		// TurnEnd, Steer, MessageStart, MessageEnd still produce no output.
		jw.handle(core.AgentEvent{Type: core.AgentEventTurnEnd})
		jw.handle(core.AgentEvent{Type: core.AgentEventSteer, Text: "hello"})
		jw.handle(core.AgentEvent{Type: core.AgentEventMessageStart})
		jw.handle(core.AgentEvent{Type: core.AgentEventMessageEnd})
	})

	if len(lines) != 0 {
		t.Fatalf("expected 0 lines for ignored events, got %d", len(lines))
	}
}

func TestJSONLineWriter_NilAssistantEvent(t *testing.T) {
	lines := captureJSONLines(t, func() {
		jw := newJSONLineWriter()
		jw.handle(core.AgentEvent{Type: core.AgentEventMessageUpdate})
	})

	if len(lines) != 0 {
		t.Fatalf("expected 0 lines for nil AssistantEvent, got %d", len(lines))
	}
}

func TestJSONLineWriter_NilResult(t *testing.T) {
	lines := captureJSONLines(t, func() {
		jw := newJSONLineWriter()
		jw.handle(core.AgentEvent{
			Type:       core.AgentEventToolExecUpdate,
			ToolCallID: "tc_1",
			Result:     nil,
		})
	})

	// nil result → no delta → no output
	if len(lines) != 0 {
		t.Fatalf("expected 0 lines for nil result, got %d", len(lines))
	}
}

func TestJSONLineWriter_Progress(t *testing.T) {
	lines := captureJSONLines(t, func() {
		jw := newJSONLineWriter()
		jw.handle(core.AgentEvent{Type: core.AgentEventTurnStart})
		jw.handle(core.AgentEvent{
			Type:     core.AgentEventToolExecStart,
			ToolName: "edit",
			Args:     map[string]any{"path": "foo.go"},
		})
		jw.handle(core.AgentEvent{
			Type:     core.AgentEventToolExecEnd,
			ToolName: "edit",
			IsError:  false,
		})
		jw.handle(core.AgentEvent{Type: core.AgentEventEnd})
	})

	// tool_execution_start, tool_execution_end, progress, summary, agent_end
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}

	// Progress after tool_execution_end
	progress := lines[2]
	if progress["type"] != "progress" {
		t.Errorf("expected progress, got %v", progress["type"])
	}
	if progress["turns"] != float64(1) {
		t.Errorf("expected turns=1, got %v", progress["turns"])
	}
	if progress["tools_completed"] != float64(1) {
		t.Errorf("expected tools_completed=1, got %v", progress["tools_completed"])
	}
	files := progress["files_touched"].([]any)
	if len(files) != 1 || files[0] != "foo.go" {
		t.Errorf("expected files_touched=[foo.go], got %v", files)
	}
	elapsed, ok := progress["elapsed_seconds"].(float64)
	if !ok || elapsed < 0 {
		t.Errorf("expected elapsed_seconds >= 0, got %v", progress["elapsed_seconds"])
	}

	// Summary before agent_end
	summary := lines[3]
	if summary["type"] != "summary" {
		t.Errorf("expected summary, got %v", summary["type"])
	}
	if summary["turns"] != float64(1) {
		t.Errorf("expected turns=1, got %v", summary["turns"])
	}

	if lines[4]["type"] != "agent_end" {
		t.Errorf("expected agent_end, got %v", lines[4]["type"])
	}
}

func TestJSONLineWriter_ProgressSkipsErrorTools(t *testing.T) {
	lines := captureJSONLines(t, func() {
		jw := newJSONLineWriter()
		jw.handle(core.AgentEvent{Type: core.AgentEventTurnStart})
		jw.handle(core.AgentEvent{
			Type:     core.AgentEventToolExecEnd,
			ToolName: "bash",
			IsError:  true,
		})
		jw.handle(core.AgentEvent{
			Type:     core.AgentEventToolExecEnd,
			ToolName: "edit",
			IsError:  false,
		})
		jw.handle(core.AgentEvent{Type: core.AgentEventEnd})
	})

	// Find the summary line
	var summary map[string]any
	for _, l := range lines {
		if l["type"] == "summary" {
			summary = l
			break
		}
	}
	if summary == nil {
		t.Fatal("no summary line found")
	}
	if summary["tools_completed"] != float64(1) {
		t.Errorf("expected tools_completed=1 (error tool excluded), got %v", summary["tools_completed"])
	}
}

func TestJSONLineWriter_TurnStartCountsInProgress(t *testing.T) {
	lines := captureJSONLines(t, func() {
		jw := newJSONLineWriter()
		jw.handle(core.AgentEvent{Type: core.AgentEventTurnStart})
		jw.handle(core.AgentEvent{Type: core.AgentEventTurnStart})
		jw.handle(core.AgentEvent{
			Type:     core.AgentEventToolExecEnd,
			ToolName: "bash",
			IsError:  false,
		})
	})

	// Find the progress line
	var progress map[string]any
	for _, l := range lines {
		if l["type"] == "progress" {
			progress = l
			break
		}
	}
	if progress == nil {
		t.Fatal("no progress line found")
	}
	if progress["turns"] != float64(2) {
		t.Errorf("expected turns=2, got %v", progress["turns"])
	}
}
