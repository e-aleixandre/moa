package tui

import (
	"testing"

	"github.com/ealeixandre/moa/pkg/bus"
)

func TestApplySubagentInner_BuildsBlocks(t *testing.T) {
	m := &appModel{s: &state{}}

	const jobID = "job-1"

	m.applySubagentInner(jobID, bus.ToolExecStarted{
		ToolCallID: "call-1",
		ToolName:   "read",
		Args:       map[string]any{"path": "foo.go"},
	})
	m.applySubagentInner(jobID, bus.ToolExecEnded{
		ToolCallID: "call-1",
		ToolName:   "read",
		Result:     "file contents",
		IsError:    false,
	})
	m.applySubagentInner(jobID, bus.TextDelta{Delta: "Hello "})
	m.applySubagentInner(jobID, bus.TextDelta{Delta: "world"})
	m.applySubagentInner(jobID, bus.MessageEnded{FullText: "Hello world"})

	tr := m.s.subagents[jobID]
	if tr == nil {
		t.Fatalf("expected transcript for %q to exist", jobID)
	}
	if len(tr.blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d: %+v", len(tr.blocks), tr.blocks)
	}

	toolBlock := tr.blocks[0]
	if toolBlock.Type != "tool" {
		t.Errorf("blocks[0].Type = %q, want %q", toolBlock.Type, "tool")
	}
	if toolBlock.ToolCallID != "call-1" || toolBlock.ToolName != "read" {
		t.Errorf("blocks[0] = %+v, want ToolCallID=call-1 ToolName=read", toolBlock)
	}
	if !toolBlock.ToolDone {
		t.Errorf("blocks[0].ToolDone = false, want true")
	}
	if toolBlock.IsError {
		t.Errorf("blocks[0].IsError = true, want false")
	}
	if toolBlock.ToolResult != "file contents" {
		t.Errorf("blocks[0].ToolResult = %q, want %q", toolBlock.ToolResult, "file contents")
	}

	assistantBlock := tr.blocks[1]
	if assistantBlock.Type != "assistant" {
		t.Errorf("blocks[1].Type = %q, want %q", assistantBlock.Type, "assistant")
	}
	if assistantBlock.Raw != "Hello world" {
		t.Errorf("blocks[1].Raw = %q, want %q", assistantBlock.Raw, "Hello world")
	}

	if tr.streamText != "" {
		t.Errorf("streamText = %q, want empty after MessageEnded", tr.streamText)
	}
}

func TestApplySubagentInner_ToolExecUpdateAccumulates(t *testing.T) {
	m := &appModel{s: &state{}}
	const jobID = "job-2"

	m.applySubagentInner(jobID, bus.ToolExecStarted{ToolCallID: "c1", ToolName: "bash"})
	m.applySubagentInner(jobID, bus.ToolExecUpdate{ToolCallID: "c1", Delta: "line1\n"})
	m.applySubagentInner(jobID, bus.ToolExecUpdate{ToolCallID: "c1", Delta: "line2\n"})
	m.applySubagentInner(jobID, bus.ToolExecEnded{ToolCallID: "c1", ToolName: "bash", Result: "line1\nline2\n"})

	tr := m.s.subagents[jobID]
	if len(tr.blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(tr.blocks))
	}
	if !tr.blocks[0].ToolDone {
		t.Errorf("expected ToolDone=true")
	}
	if tr.blocks[0].ToolResult != "line1\nline2\n" {
		t.Errorf("ToolResult = %q, want authoritative Result from ToolExecEnded", tr.blocks[0].ToolResult)
	}
}

func TestHandleSubagentStartedAndEnded(t *testing.T) {
	m := &appModel{s: &state{}}

	m.handleSubagentStarted(bus.SubagentStarted{
		JobID: "j1", Task: "fix bug", Model: "gpt-5", Async: true,
	})
	tr := m.s.subagents["j1"]
	if tr == nil {
		t.Fatalf("expected transcript to exist after SubagentStarted")
	}
	if tr.status != "running" || tr.task != "fix bug" || tr.model != "gpt-5" || !tr.async {
		t.Errorf("unexpected transcript state: %+v", tr)
	}

	if !m.hasLiveSubagents() {
		t.Errorf("hasLiveSubagents() = false, want true while running")
	}

	m.handleSubagentEnded(bus.SubagentEnded{JobID: "j1", Status: "completed"})
	if tr.status != "completed" {
		t.Errorf("status = %q, want completed", tr.status)
	}
	if m.hasLiveSubagents() {
		t.Errorf("hasLiveSubagents() = true, want false after completion")
	}
}

func TestBashJobTranscript(t *testing.T) {
	m := &appModel{s: &state{}}
	m.handleBashJobStarted(bus.BashJobStarted{JobID: "bash-1", Command: "go test ./...", CWD: "/work"})
	m.handleBashJobOutput(bus.BashJobOutput{JobID: "bash-1", Delta: "running\n"})
	m.handleBashJobEnded(bus.BashJobEnded{JobID: "bash-1", Status: "completed", Output: "done\n"})
	transcript := m.s.subagents["bash-1"]
	if transcript == nil || transcript.kind != "bash" || transcript.status != "completed" {
		t.Fatalf("transcript = %+v", transcript)
	}
	if len(transcript.blocks) != 1 || !transcript.blocks[0].ToolDone || transcript.blocks[0].ToolResult != "done\n" {
		t.Fatalf("blocks = %+v", transcript.blocks)
	}
}

// TestHandleSubagentStarted_DoesNotResurrectTerminal covers the promote/finish
// race: a promotion's SubagentStarted(Async:true) can arrive AFTER the
// SubagentEnded that already marked the job terminal. It must not flip the job
// back to "running".
func TestHandleSubagentStarted_DoesNotResurrectTerminal(t *testing.T) {
	m := &appModel{s: &state{}}

	m.handleSubagentStarted(bus.SubagentStarted{JobID: "j1", Task: "t", Model: "m", Async: false})
	m.handleSubagentEnded(bus.SubagentEnded{JobID: "j1", Status: "completed"})
	// Late promotion echo.
	m.handleSubagentStarted(bus.SubagentStarted{JobID: "j1", Async: true})

	tr := m.s.subagents["j1"]
	if tr.status != "completed" {
		t.Errorf("status = %q, want completed (must not resurrect to running)", tr.status)
	}
	if !tr.async {
		t.Errorf("async = false, want true (flag should still update)")
	}
	if m.hasLiveSubagents() {
		t.Errorf("hasLiveSubagents() = true, want false for a completed job")
	}
}

func TestSubagentPicker_OnlyListsLiveEntries(t *testing.T) {
	subs := map[string]*subagentTranscript{
		"running1":  {jobID: "running1", task: "task A", status: "running"},
		"done1":     {jobID: "done1", task: "task B", status: "completed"},
		"failed1":   {jobID: "failed1", task: "task C", status: "failed"},
		"running2":  {jobID: "running2", task: "task D", status: "running"},
		"cancelled": {jobID: "cancelled", task: "task E", status: "cancelled"},
	}

	var p subagentPicker
	p.Open(subs)

	if len(p.entries) != 2 {
		t.Fatalf("expected 2 live entries, got %d: %+v", len(p.entries), p.entries)
	}
	for _, e := range p.entries {
		if e.status != "running" {
			t.Errorf("unexpected entry with status %q in picker", e.status)
		}
	}

	if got := p.Selected(); got != "running1" && got != "running2" {
		t.Errorf("Selected() = %q, want one of the live job IDs", got)
	}
}
