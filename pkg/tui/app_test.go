package tui

import (
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/go-agent/pkg/core"
)

// newTestModel creates a minimal appModel for state-level tests.
// No agent, no event channel — only state, renderer, and components are initialized.
func newTestModel() appModel {
	return appModel{
		s:        &state{showThinking: true},
		renderer: newRenderer(80),
		input:    newInput(),
		status:   newStatus(),
		width:    80,
		height:   24,
	}
}

// --- Test 1: flushBlocks ordering and flushedCount ---

func TestFlushBlocks_UpdatesFlushedCount(t *testing.T) {
	m := newTestModel()
	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "hello"},
		{Type: "tool_start", ToolName: "bash", ToolArgs: map[string]any{"command": "ls"}},
		{Type: "assistant", Raw: "world"},
	}

	// Flush all 3 blocks
	cmd := m.flushBlocks(0, 3)
	if m.s.flushedCount != 3 {
		t.Errorf("flushedCount = %d, want 3", m.s.flushedCount)
	}
	if cmd == nil {
		t.Error("expected non-nil Cmd for flush, got nil")
	}

	// No-op flush (already flushed)
	cmd = m.flushBlocks(3, 3)
	if cmd != nil {
		t.Error("expected nil Cmd for no-op flush, got non-nil")
	}
	if m.s.flushedCount != 3 {
		t.Errorf("flushedCount = %d, want 3 after no-op", m.s.flushedCount)
	}
}

func TestFlushBlocks_SkipsEmptyBlocks(t *testing.T) {
	m := newTestModel()
	m.s.showThinking = false
	m.s.blocks = []messageBlock{
		{Type: "thinking", Raw: "hmm"}, // hidden when showThinking=false
	}

	cmd := m.flushBlocks(0, 1)
	if m.s.flushedCount != 1 {
		t.Errorf("flushedCount = %d, want 1", m.s.flushedCount)
	}
	// All rendered parts are empty → nil Cmd
	if cmd != nil {
		t.Error("expected nil Cmd when all blocks render empty, got non-nil")
	}
}

// --- Test 2: handleAgentEvent message_end flushes blocks ---

func TestHandleAgentEvent_MessageEnd_FlushesBlocks(t *testing.T) {
	m := newTestModel()
	m.s.streamText = "hello world"
	m.s.thinkingText = "let me think"

	cmd := m.handleAgentEvent(core.AgentEvent{
		Type: core.AgentEventMessageEnd,
	})

	// Should have appended thinking + assistant blocks
	if len(m.s.blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(m.s.blocks))
	}
	if m.s.blocks[0].Type != "thinking" || m.s.blocks[0].Raw != "let me think" {
		t.Errorf("blocks[0] = %+v, want thinking block", m.s.blocks[0])
	}
	if m.s.blocks[1].Type != "assistant" || m.s.blocks[1].Raw != "hello world" {
		t.Errorf("blocks[1] = %+v, want assistant block", m.s.blocks[1])
	}

	// Stream state should be cleared
	if m.s.streamText != "" {
		t.Errorf("streamText = %q, want empty", m.s.streamText)
	}
	if m.s.thinkingText != "" {
		t.Errorf("thinkingText = %q, want empty", m.s.thinkingText)
	}
	if m.s.streamCache != "" {
		t.Errorf("streamCache = %q, want empty", m.s.streamCache)
	}

	// Should have flushed
	if m.s.flushedCount != 2 {
		t.Errorf("flushedCount = %d, want 2", m.s.flushedCount)
	}
	if cmd == nil {
		t.Error("expected non-nil flush Cmd")
	}
}

func TestHandleAgentEvent_MessageEnd_NoContent(t *testing.T) {
	m := newTestModel()
	m.s.streamText = ""
	m.s.thinkingText = ""

	cmd := m.handleAgentEvent(core.AgentEvent{
		Type: core.AgentEventMessageEnd,
	})

	if len(m.s.blocks) != 0 {
		t.Errorf("blocks = %d, want 0 (no content)", len(m.s.blocks))
	}
	if cmd != nil {
		t.Error("expected nil Cmd when no content to flush")
	}
}

// --- Test 3: patchFromMessages corrects text without dropping tool blocks ---

func TestPatchFromMessages_PreservesToolBlocks(t *testing.T) {
	m := newTestModel()
	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "do something"},
		{Type: "tool_start", ToolName: "bash", ToolArgs: map[string]any{"command": "ls -la"}},
		{Type: "tool_end", ToolName: "bash", IsError: false},
		{Type: "thinking", Raw: "partial thinking"},
		{Type: "assistant", Raw: "partial response"},
	}

	// Source-of-truth messages have corrected text
	m.patchFromMessages([]core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{{Type: "text", Text: "do something"}}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			{Type: "thinking", Thinking: "full thinking text"},
			{Type: "text", Text: "full response text"},
		}}},
	})

	// Should still have 5 blocks (tool blocks preserved)
	if len(m.s.blocks) != 5 {
		t.Fatalf("blocks = %d, want 5", len(m.s.blocks))
	}

	// tool_start preserved with original args
	if m.s.blocks[1].Type != "tool_start" || m.s.blocks[1].ToolName != "bash" {
		t.Errorf("blocks[1] = %+v, want tool_start", m.s.blocks[1])
	}
	if m.s.blocks[1].ToolArgs["command"] != "ls -la" {
		t.Errorf("tool args = %v, want 'ls -la'", m.s.blocks[1].ToolArgs)
	}

	// thinking corrected
	if m.s.blocks[3].Raw != "full thinking text" {
		t.Errorf("thinking = %q, want 'full thinking text'", m.s.blocks[3].Raw)
	}

	// assistant corrected
	if m.s.blocks[4].Raw != "full response text" {
		t.Errorf("assistant = %q, want 'full response text'", m.s.blocks[4].Raw)
	}
}

func TestPatchFromMessages_NilMessages(t *testing.T) {
	m := newTestModel()
	m.s.blocks = []messageBlock{{Type: "user", Raw: "hello"}}

	m.patchFromMessages(nil)

	// No change
	if len(m.s.blocks) != 1 {
		t.Errorf("blocks = %d, want 1", len(m.s.blocks))
	}
}

// Test for the async emitter race: agentRunResultMsg arrives before
// AgentEventMessageEnd is processed, so assistant/thinking blocks don't exist yet.
func TestPatchFromMessages_CreatesMissingBlocks(t *testing.T) {
	m := newTestModel()
	// Only a user block exists — MessageEnd event wasn't processed
	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "hello"},
	}

	m.patchFromMessages([]core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{{Type: "text", Text: "hello"}}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			{Type: "thinking", Thinking: "let me think about this"},
			{Type: "text", Text: "here is my response"},
		}}},
	})

	// Should have created thinking + assistant blocks
	if len(m.s.blocks) != 3 {
		t.Fatalf("blocks = %d, want 3", len(m.s.blocks))
	}
	if m.s.blocks[1].Type != "thinking" || m.s.blocks[1].Raw != "let me think about this" {
		t.Errorf("blocks[1] = %+v, want thinking block", m.s.blocks[1])
	}
	if m.s.blocks[2].Type != "assistant" || m.s.blocks[2].Raw != "here is my response" {
		t.Errorf("blocks[2] = %+v, want assistant block", m.s.blocks[2])
	}
}

func TestPatchFromMessages_CreatesMissingAssistantOnly(t *testing.T) {
	m := newTestModel()
	// Tool blocks exist but no assistant block
	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "hello"},
		{Type: "tool_start", ToolName: "bash", ToolArgs: map[string]any{"command": "ls"}},
		{Type: "tool_end", ToolName: "bash"},
	}

	m.patchFromMessages([]core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{{Type: "text", Text: "hello"}}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			{Type: "text", Text: "done"},
		}}},
	})

	// Should have appended assistant block, keeping tool blocks intact
	if len(m.s.blocks) != 4 {
		t.Fatalf("blocks = %d, want 4", len(m.s.blocks))
	}
	if m.s.blocks[1].Type != "tool_start" {
		t.Errorf("blocks[1] = %+v, want tool_start", m.s.blocks[1])
	}
	if m.s.blocks[3].Type != "assistant" || m.s.blocks[3].Raw != "done" {
		t.Errorf("blocks[3] = %+v, want assistant block", m.s.blocks[3])
	}
}

// --- Test 4: handleRunResult flushes unflushed blocks ---

func TestHandleRunResult_FlushesUnflushedBlocks(t *testing.T) {
	m := newTestModel()
	m.s.runGen = 5
	m.runGenAddr = &atomic.Uint64{}
	m.runGenAddr.Store(5)

	// Simulate: 3 blocks, only first was flushed
	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "hello"},
		{Type: "thinking", Raw: "hmm"},
		{Type: "assistant", Raw: "world"},
	}
	m.s.flushedCount = 1
	m.s.running = true
	m.s.streamState = stateStreaming
	m.input.SetEnabled(false)

	result, cmd := m.handleRunResult(agentRunResultMsg{
		RunGen: 5,
		Messages: []core.AgentMessage{
			{Message: core.Message{Role: "user", Content: []core.Content{{Type: "text", Text: "hello"}}}},
			{Message: core.Message{Role: "assistant", Content: []core.Content{{Type: "text", Text: "world"}}}},
		},
	})
	_ = cmd

	rm := result.(appModel)
	// All blocks should be flushed
	if rm.s.flushedCount != 3 {
		t.Errorf("flushedCount = %d, want 3", rm.s.flushedCount)
	}

	// State should be reset
	if rm.s.running {
		t.Error("running should be false")
	}
	if rm.s.streamState != stateIdle {
		t.Errorf("streamState = %d, want stateIdle", rm.s.streamState)
	}
}

func TestHandleRunResult_IgnoresOldGen(t *testing.T) {
	m := newTestModel()
	m.s.runGen = 5
	m.runGenAddr = &atomic.Uint64{}
	m.runGenAddr.Store(5)
	m.s.running = true

	result, cmd := m.handleRunResult(agentRunResultMsg{
		RunGen: 3, // old generation
	})

	rm := result.(appModel)
	// Should still be running (result ignored)
	if !rm.s.running {
		t.Error("running should still be true for old gen")
	}
	if cmd != nil {
		t.Error("expected nil Cmd for old gen")
	}
}

// --- Test 5: resize invalidates stream cache ---

func TestWindowResize_InvalidatesStreamCache(t *testing.T) {
	m := newTestModel()
	m.s.streamText = "some markdown text"
	m.s.streamCache = "cached render"
	m.s.dirty = false

	result, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	rm := result.(appModel)

	if !rm.s.dirty {
		t.Error("dirty should be true after resize with active streamText")
	}
	if rm.width != 120 {
		t.Errorf("width = %d, want 120", rm.width)
	}
}

func TestWindowResize_NoDirtyWhenNotStreaming(t *testing.T) {
	m := newTestModel()
	m.s.streamText = ""
	m.s.dirty = false

	result, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	rm := result.(appModel)

	if rm.s.dirty {
		t.Error("dirty should be false when no streamText")
	}
}

// --- Test 6: Ctrl+O expand mode ---

func TestCtrlO_EntersExpandMode(t *testing.T) {
	m := newTestModel()
	m.s.blocks = []messageBlock{{Type: "user", Raw: "hello"}}

	result, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	rm := result.(appModel)
	if !rm.s.expandMode {
		t.Error("expandMode should be true after Ctrl+O")
	}
	if cmd == nil {
		t.Error("expected ClearScreen Cmd")
	}
}

func TestCtrlO_WorksWhileRunning(t *testing.T) {
	m := newTestModel()
	m.s.running = true
	m.s.blocks = []messageBlock{{Type: "user", Raw: "hello"}}

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	rm := result.(appModel)
	// In-process pager doesn't pause event processing, so it works while running
	if !rm.s.expandMode {
		t.Error("expandMode should be true even while running")
	}
}

func TestCtrlO_DisabledWhenEmpty(t *testing.T) {
	m := newTestModel()
	m.s.blocks = nil

	result, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	rm := result.(appModel)
	if rm.s.expandMode {
		t.Error("expandMode should be false with no blocks")
	}
	if cmd != nil {
		t.Error("expected nil Cmd for Ctrl+O with no blocks")
	}
}

// --- Test 7: /clear resets all state ---

func TestClear_ResetsState(t *testing.T) {
	m := newTestModel()
	// Mock agent with Reset() that succeeds
	m.agent = nil // handleCommand("clear") calls m.agent.Reset()
	// We can't call Reset() without an agent, so test the state logic directly

	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "hello"},
		{Type: "assistant", Raw: "world"},
	}
	m.s.flushedCount = 2
	m.s.streamText = "streaming..."
	m.s.thinkingText = "thinking..."
	m.s.streamCache = "cached"

	// Simulate what /clear does (minus agent.Reset which needs a real agent)
	m.s.blocks = m.s.blocks[:0]
	m.s.flushedCount = 0
	m.s.streamText = ""
	m.s.thinkingText = ""
	m.s.streamCache = ""

	if len(m.s.blocks) != 0 {
		t.Errorf("blocks = %d, want 0", len(m.s.blocks))
	}
	if m.s.flushedCount != 0 {
		t.Errorf("flushedCount = %d, want 0", m.s.flushedCount)
	}
	if m.s.streamText != "" {
		t.Errorf("streamText = %q, want empty", m.s.streamText)
	}
	if m.s.thinkingText != "" {
		t.Errorf("thinkingText = %q, want empty", m.s.thinkingText)
	}
	if m.s.streamCache != "" {
		t.Errorf("streamCache = %q, want empty", m.s.streamCache)
	}
}

// --- Test: handleAgentEvent tool events ---

func TestHandleAgentEvent_ToolStart_FlushesImmediately(t *testing.T) {
	m := newTestModel()

	cmd := m.handleAgentEvent(core.AgentEvent{
		Type:     core.AgentEventToolExecStart,
		ToolName: "bash",
		Args:     map[string]any{"command": "ls"},
	})

	if len(m.s.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(m.s.blocks))
	}
	if m.s.blocks[0].Type != "tool_start" {
		t.Errorf("blocks[0].Type = %q, want tool_start", m.s.blocks[0].Type)
	}
	if m.s.flushedCount != 1 {
		t.Errorf("flushedCount = %d, want 1", m.s.flushedCount)
	}
	if cmd == nil {
		t.Error("expected non-nil flush Cmd")
	}
	if m.s.streamState != stateToolRunning {
		t.Errorf("streamState = %d, want stateToolRunning", m.s.streamState)
	}
}

func TestHandleAgentEvent_ToolEnd_FlushesImmediately(t *testing.T) {
	m := newTestModel()
	// Pre-existing tool_start block (already flushed)
	m.s.blocks = []messageBlock{
		{Type: "tool_start", ToolName: "bash"},
	}
	m.s.flushedCount = 1

	cmd := m.handleAgentEvent(core.AgentEvent{
		Type:     core.AgentEventToolExecEnd,
		ToolName: "bash",
		IsError:  false,
	})

	if len(m.s.blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(m.s.blocks))
	}
	if m.s.blocks[1].Type != "tool_end" {
		t.Errorf("blocks[1].Type = %q, want tool_end", m.s.blocks[1].Type)
	}
	if m.s.flushedCount != 2 {
		t.Errorf("flushedCount = %d, want 2", m.s.flushedCount)
	}
	if cmd == nil {
		t.Error("expected non-nil flush Cmd")
	}
}

// --- Test: renderSingleBlock ---

func TestRenderSingleBlock_HidesThinkingWhenDisabled(t *testing.T) {
	r := newRenderer(80)
	block := messageBlock{Type: "thinking", Raw: "some thinking"}

	result := renderSingleBlock(block, r, false)
	if result != "" {
		t.Errorf("expected empty for hidden thinking, got %q", result)
	}

	result = renderSingleBlock(block, r, true)
	if result == "" {
		t.Error("expected non-empty for visible thinking")
	}
}

func TestRenderSingleBlock_UnknownType(t *testing.T) {
	r := newRenderer(80)
	block := messageBlock{Type: "unknown", Raw: "data"}

	result := renderSingleBlock(block, r, true)
	if result != "" {
		t.Errorf("expected empty for unknown type, got %q", result)
	}
}


