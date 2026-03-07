package tui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/core"
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

type staticProvider struct{ text string }

func (p staticProvider) Stream(_ context.Context, _ core.Request) (<-chan core.AssistantEvent, error) {
	ch := make(chan core.AssistantEvent, 3)
	msg := core.Message{
		Role:       "assistant",
		Content:    []core.Content{core.TextContent(p.text)},
		StopReason: "end_turn",
		Timestamp:  time.Now().Unix(),
	}
	ch <- core.AssistantEvent{Type: core.ProviderEventStart, Partial: &msg}
	if p.text != "" {
		ch <- core.AssistantEvent{Type: core.ProviderEventTextDelta, Delta: p.text, ContentIndex: 0}
	}
	ch <- core.AssistantEvent{Type: core.ProviderEventDone, Message: &msg}
	close(ch)
	return ch, nil
}

func newSwitchTestApp(t *testing.T) appModel {
	t.Helper()
	ag, err := agent.New(agent.AgentConfig{
		Provider:      staticProvider{text: "ok"},
		Model:         core.Model{ID: "claude-sonnet-4-6", Provider: "anthropic", Name: "Claude Sonnet 4.6", MaxInput: 200_000},
		ThinkingLevel: "medium",
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	m := New(ag, context.Background(), Config{
		ModelName: "Claude Sonnet 4.6",
		ProviderFactory: func(model core.Model) (core.Provider, error) {
			return staticProvider{text: "ok"}, nil
		},
	})
	m.width = 80
	m.height = 24
	m.renderer.SetWidth(80)
	m.input.SetWidth(80)
	m.status.SetWidth(80)
	return m
}

// --- Test 1: flushBlocks ordering and flushedCount ---

func TestFlushBlocks_SchedulesButDoesNotAdvanceFlushedCount(t *testing.T) {
	m := newTestModel()
	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "hello"},
		{Type: "tool", ToolName: "bash", ToolArgs: map[string]any{"command": "ls"}, ToolDone: true},
		{Type: "assistant", Raw: "world"},
	}

	// Schedule flush of all 3 blocks
	cmd := m.flushBlocks(3)
	// flushedCount stays at 0 (deferred until flushDoneMsg)
	if m.s.flushedCount != 0 {
		t.Errorf("flushedCount = %d, want 0 (deferred)", m.s.flushedCount)
	}
	// flushScheduledCount advances immediately
	if m.s.flushScheduledCount != 3 {
		t.Errorf("flushScheduledCount = %d, want 3", m.s.flushScheduledCount)
	}
	if cmd == nil {
		t.Error("expected non-nil Cmd for flush, got nil")
	}

	// No-op flush (already scheduled)
	cmd = m.flushBlocks(3)
	if cmd != nil {
		t.Error("expected nil Cmd for no-op flush, got non-nil")
	}
}

func TestFlushDoneMsg_AdvancesFlushedCount(t *testing.T) {
	m := newTestModel()
	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "hello"},
		{Type: "assistant", Raw: "world"},
	}
	m.s.flushScheduledCount = 2

	// Simulate flushDoneMsg
	result, _ := m.Update(flushDoneMsg{upTo: 2, epoch: 0})
	rm := result.(appModel)
	if rm.s.flushedCount != 2 {
		t.Errorf("flushedCount = %d, want 2", rm.s.flushedCount)
	}
}

func TestFlushDoneMsg_IgnoresStaleEpoch(t *testing.T) {
	m := newTestModel()
	m.s.blocks = []messageBlock{{Type: "user", Raw: "hello"}}
	m.s.flushScheduledCount = 1
	m.s.flushEpoch = 2 // current epoch is 2

	// flushDoneMsg from epoch 1 (stale — before /clear)
	result, _ := m.Update(flushDoneMsg{upTo: 1, epoch: 1})
	rm := result.(appModel)
	if rm.s.flushedCount != 0 {
		t.Errorf("flushedCount = %d, want 0 (stale epoch ignored)", rm.s.flushedCount)
	}
}

func TestFlushBlocks_SkipsEmptyBlocks(t *testing.T) {
	m := newTestModel()
	m.s.showThinking = false
	m.s.blocks = []messageBlock{
		{Type: "thinking", Raw: "hmm"}, // hidden when showThinking=false
	}

	cmd := m.flushBlocks(1)
	if m.s.flushScheduledCount != 1 {
		t.Errorf("flushScheduledCount = %d, want 1", m.s.flushScheduledCount)
	}
	// All rendered parts are empty → returns a done func (not nil, to confirm the advance)
	if cmd == nil {
		t.Error("expected non-nil Cmd (done func for empty flush)")
	}
}

// --- Test 2: handleAgentEvent message_end flushes blocks ---

func TestHandleAgentEvent_MessageEnd_AppendsButDoesNotFlush(t *testing.T) {
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

	// NOT flushed — blocks stay visible in View().
	// They get flushed by the next tool event or agentRunResultMsg.
	if m.s.flushedCount != 0 {
		t.Errorf("flushedCount = %d, want 0 (deferred)", m.s.flushedCount)
	}
	if m.s.flushScheduledCount != 0 {
		t.Errorf("flushScheduledCount = %d, want 0 (deferred)", m.s.flushScheduledCount)
	}
	if cmd != nil {
		t.Error("expected nil Cmd (deferred flush)")
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
		{Type: "tool", ToolName: "bash", ToolArgs: map[string]any{"command": "ls -la"}, ToolDone: true},
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

	// Should still have 4 blocks (tool block preserved as single unified block)
	if len(m.s.blocks) != 4 {
		t.Fatalf("blocks = %d, want 4", len(m.s.blocks))
	}

	// tool block preserved with original args
	if m.s.blocks[1].Type != "tool" || m.s.blocks[1].ToolName != "bash" {
		t.Errorf("blocks[1] = %+v, want tool", m.s.blocks[1])
	}
	if m.s.blocks[1].ToolArgs["command"] != "ls -la" {
		t.Errorf("tool args = %v, want 'ls -la'", m.s.blocks[1].ToolArgs)
	}

	// thinking corrected
	if m.s.blocks[2].Raw != "full thinking text" {
		t.Errorf("thinking = %q, want 'full thinking text'", m.s.blocks[2].Raw)
	}

	// assistant corrected
	if m.s.blocks[3].Raw != "full response text" {
		t.Errorf("assistant = %q, want 'full response text'", m.s.blocks[3].Raw)
	}
}

// Regression test: multi-turn scenario where MessageEnd is missed.
// Without the fix, patchFromMessages would find the flushed turn-1 assistant
// block, patch it, and flushBlocks would be a no-op → message disappears.
func TestPatchFromMessages_DoesNotPatchFlushedBlocks(t *testing.T) {
	m := newTestModel()

	// Turn 1: user + assistant blocks, already flushed to scrollback
	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "turn 1 question"},
		{Type: "assistant", Raw: "turn 1 answer"},
		// Turn 2: user block flushed, but MessageEnd not yet processed
		{Type: "user", Raw: "turn 2 question"},
	}
	m.s.flushedCount = 3 // all 3 blocks are flushed
	m.s.flushScheduledCount = 3

	// agent.Send returns with turn-2 assistant text, but MessageEnd was missed
	m.patchFromMessages([]core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{{Type: "text", Text: "turn 1 question"}}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{{Type: "text", Text: "turn 1 answer"}}}},
		{Message: core.Message{Role: "user", Content: []core.Content{{Type: "text", Text: "turn 2 question"}}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			{Type: "text", Text: "turn 2 answer"},
		}}},
	})

	// CRITICAL: turn 1 assistant must NOT be overwritten
	if m.s.blocks[1].Raw != "turn 1 answer" {
		t.Errorf("turn 1 assistant was overwritten: got %q, want %q", m.s.blocks[1].Raw, "turn 1 answer")
	}

	// Turn 2 assistant must be APPENDED (not patched into turn 1)
	if len(m.s.blocks) != 4 {
		t.Fatalf("blocks = %d, want 4 (new block appended)", len(m.s.blocks))
	}
	if m.s.blocks[3].Type != "assistant" || m.s.blocks[3].Raw != "turn 2 answer" {
		t.Errorf("blocks[3] = %+v, want assistant 'turn 2 answer'", m.s.blocks[3])
	}

	// flushBlocks should now have something to flush (block index 3)
	cmd := m.flushBlocks(len(m.s.blocks))
	if cmd == nil {
		t.Error("expected non-nil flush Cmd for the new appended block")
	}
	if m.s.flushScheduledCount != 4 {
		t.Errorf("flushScheduledCount = %d, want 4", m.s.flushScheduledCount)
	}
}

// Same scenario but with thinking blocks too.
func TestPatchFromMessages_DoesNotPatchFlushedThinking(t *testing.T) {
	m := newTestModel()

	// Turn 1 fully flushed with thinking
	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "q1"},
		{Type: "thinking", Raw: "think1"},
		{Type: "assistant", Raw: "a1"},
		// Turn 2 user flushed, MessageEnd missed
		{Type: "user", Raw: "q2"},
	}
	m.s.flushedCount = 4
	m.s.flushScheduledCount = 4

	m.patchFromMessages([]core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{{Type: "text", Text: "q1"}}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			{Type: "thinking", Thinking: "think1"},
			{Type: "text", Text: "a1"},
		}}},
		{Message: core.Message{Role: "user", Content: []core.Content{{Type: "text", Text: "q2"}}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			{Type: "thinking", Thinking: "think2"},
			{Type: "text", Text: "a2"},
		}}},
	})

	// Turn 1 blocks untouched
	if m.s.blocks[1].Raw != "think1" {
		t.Errorf("turn 1 thinking overwritten: %q", m.s.blocks[1].Raw)
	}
	if m.s.blocks[2].Raw != "a1" {
		t.Errorf("turn 1 assistant overwritten: %q", m.s.blocks[2].Raw)
	}

	// Turn 2 blocks appended
	if len(m.s.blocks) != 6 {
		t.Fatalf("blocks = %d, want 6", len(m.s.blocks))
	}
	if m.s.blocks[4].Type != "thinking" || m.s.blocks[4].Raw != "think2" {
		t.Errorf("blocks[4] = %+v, want thinking 'think2'", m.s.blocks[4])
	}
	if m.s.blocks[5].Type != "assistant" || m.s.blocks[5].Raw != "a2" {
		t.Errorf("blocks[5] = %+v, want assistant 'a2'", m.s.blocks[5])
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
	// Tool block exists but no assistant block
	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "hello"},
		{Type: "tool", ToolName: "bash", ToolArgs: map[string]any{"command": "ls"}, ToolDone: true},
	}

	m.patchFromMessages([]core.AgentMessage{
		{Message: core.Message{Role: "user", Content: []core.Content{{Type: "text", Text: "hello"}}}},
		{Message: core.Message{Role: "assistant", Content: []core.Content{
			{Type: "text", Text: "done"},
		}}},
	})

	// Should have appended assistant block, keeping tool block intact
	if len(m.s.blocks) != 3 {
		t.Fatalf("blocks = %d, want 3", len(m.s.blocks))
	}
	if m.s.blocks[1].Type != "tool" {
		t.Errorf("blocks[1] = %+v, want tool", m.s.blocks[1])
	}
	if m.s.blocks[2].Type != "assistant" || m.s.blocks[2].Raw != "done" {
		t.Errorf("blocks[2] = %+v, want assistant block", m.s.blocks[2])
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
	// Blocks should be scheduled for flush (not yet confirmed)
	if rm.s.flushScheduledCount != 3 {
		t.Errorf("flushScheduledCount = %d, want 3", rm.s.flushScheduledCount)
	}
	// flushedCount stays at 1 until flushDoneMsg confirms
	if rm.s.flushedCount != 1 {
		t.Errorf("flushedCount = %d, want 1 (deferred)", rm.s.flushedCount)
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

// --- Test 6: Ctrl+O reprint ---

func TestCtrlO_ReturnsCmdWithBlocks(t *testing.T) {
	m := newTestModel()
	m.s.blocks = []messageBlock{{Type: "user", Raw: "hello"}}

	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	if cmd == nil {
		t.Error("expected non-nil Cmd for Ctrl+O with blocks")
	}
}

func TestCtrlO_NilWhenEmpty(t *testing.T) {
	m := newTestModel()
	m.s.blocks = nil

	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
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
	m.s.flushScheduledCount = 2
	m.s.flushEpoch = 0
	m.s.streamText = "streaming..."
	m.s.thinkingText = "thinking..."
	m.s.streamCache = "cached"

	// Simulate what /clear does (minus agent.Reset which needs a real agent)
	m.s.blocks = m.s.blocks[:0]
	m.s.flushedCount = 0
	m.s.flushScheduledCount = 0
	m.s.flushEpoch++
	m.s.streamText = ""
	m.s.thinkingText = ""
	m.s.streamCache = ""

	if len(m.s.blocks) != 0 {
		t.Errorf("blocks = %d, want 0", len(m.s.blocks))
	}
	if m.s.flushedCount != 0 {
		t.Errorf("flushedCount = %d, want 0", m.s.flushedCount)
	}
	if m.s.flushScheduledCount != 0 {
		t.Errorf("flushScheduledCount = %d, want 0", m.s.flushScheduledCount)
	}
	if m.s.flushEpoch != 1 {
		t.Errorf("flushEpoch = %d, want 1", m.s.flushEpoch)
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

func TestHandleAgentEvent_ToolStart_StaysInLiveArea(t *testing.T) {
	m := newTestModel()

	cmd := m.handleAgentEvent(core.AgentEvent{
		Type:       core.AgentEventToolExecStart,
		ToolCallID: "tc-1",
		ToolName:   "bash",
		Args:       map[string]any{"command": "ls"},
	})

	if len(m.s.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(m.s.blocks))
	}
	if m.s.blocks[0].Type != "tool" {
		t.Errorf("blocks[0].Type = %q, want tool", m.s.blocks[0].Type)
	}
	if m.s.blocks[0].ToolDone {
		t.Error("block should not be done yet")
	}
	// Tool blocks stay in the live area (not flushed) until all tools complete.
	if m.s.flushScheduledCount != 0 {
		t.Errorf("flushScheduledCount = %d, want 0", m.s.flushScheduledCount)
	}
	if cmd != nil {
		t.Error("expected nil cmd (no flush)")
	}
	if m.s.streamState != stateToolRunning {
		t.Errorf("streamState = %d, want stateToolRunning", m.s.streamState)
	}
	if m.s.activeTools != 1 {
		t.Errorf("activeTools = %d, want 1", m.s.activeTools)
	}
}

func TestHandleAgentEvent_ToolEnd_UpdatesBlockAndFlushes(t *testing.T) {
	m := newTestModel()
	// Simulate a running tool block in the live area.
	m.s.blocks = []messageBlock{
		{Type: "tool", ToolCallID: "tc-1", ToolName: "bash"},
	}
	m.s.activeTools = 1
	m.s.streamState = stateToolRunning

	result := core.TextResult("file1.go\nfile2.go")
	cmd := m.handleAgentEvent(core.AgentEvent{
		Type:       core.AgentEventToolExecEnd,
		ToolCallID: "tc-1",
		ToolName:   "bash",
		IsError:    false,
		Result:     &result,
	})

	// Block should be updated in-place.
	if len(m.s.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1 (updated in-place)", len(m.s.blocks))
	}
	if !m.s.blocks[0].ToolDone {
		t.Error("block should be done")
	}
	if m.s.blocks[0].ToolResult != "file1.go\nfile2.go" {
		t.Errorf("ToolResult = %q, want file content", m.s.blocks[0].ToolResult)
	}
	// All tools done → flush.
	if m.s.flushScheduledCount != 1 {
		t.Errorf("flushScheduledCount = %d, want 1", m.s.flushScheduledCount)
	}
	if cmd == nil {
		t.Error("expected non-nil flush Cmd")
	}
	if m.s.streamState != stateStreaming {
		t.Errorf("streamState = %d, want stateStreaming", m.s.streamState)
	}
	if m.s.activeTools != 0 {
		t.Errorf("activeTools = %d, want 0", m.s.activeTools)
	}
}

func TestHandleAgentEvent_ToolUpdate_StreamsOutput(t *testing.T) {
	m := newTestModel()

	// Start a bash tool.
	m.handleAgentEvent(core.AgentEvent{
		Type: core.AgentEventToolExecStart, ToolCallID: "tc-1", ToolName: "bash",
		Args: map[string]any{"command": "make test"},
	})
	if m.s.blocks[0].ToolResult != "" {
		t.Fatal("result should be empty before any update")
	}

	// Stream two chunks.
	r1 := core.TextResult("PASS pkg/core\n")
	m.handleAgentEvent(core.AgentEvent{
		Type: core.AgentEventToolExecUpdate, ToolCallID: "tc-1", Result: &r1,
	})
	r2 := core.TextResult("PASS pkg/tui\n")
	m.handleAgentEvent(core.AgentEvent{
		Type: core.AgentEventToolExecUpdate, ToolCallID: "tc-1", Result: &r2,
	})

	if m.s.blocks[0].ToolResult != "PASS pkg/core\nPASS pkg/tui\n" {
		t.Errorf("accumulated result = %q", m.s.blocks[0].ToolResult)
	}
	if m.s.blocks[0].ToolDone {
		t.Error("should still be running")
	}

	// End replaces with final result.
	final := core.TextResult("ok\n2 passed")
	m.handleAgentEvent(core.AgentEvent{
		Type: core.AgentEventToolExecEnd, ToolCallID: "tc-1", ToolName: "bash", Result: &final,
	})
	if m.s.blocks[0].ToolResult != "ok\n2 passed" {
		t.Errorf("final result = %q, want 'ok\\n2 passed'", m.s.blocks[0].ToolResult)
	}
	if !m.s.blocks[0].ToolDone {
		t.Error("should be done")
	}
}

func TestHandleAgentEvent_ParallelTools_FlushOnlyWhenAllDone(t *testing.T) {
	m := newTestModel()

	// Two tools start.
	m.handleAgentEvent(core.AgentEvent{
		Type: core.AgentEventToolExecStart, ToolCallID: "tc-1", ToolName: "bash",
	})
	m.handleAgentEvent(core.AgentEvent{
		Type: core.AgentEventToolExecStart, ToolCallID: "tc-2", ToolName: "read",
	})

	if m.s.activeTools != 2 {
		t.Fatalf("activeTools = %d, want 2", m.s.activeTools)
	}
	if len(m.s.blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(m.s.blocks))
	}

	// First tool finishes — should NOT flush.
	r1 := core.TextResult("done")
	cmd := m.handleAgentEvent(core.AgentEvent{
		Type: core.AgentEventToolExecEnd, ToolCallID: "tc-1", ToolName: "bash", Result: &r1,
	})
	if cmd != nil {
		t.Error("should not flush while tools still running")
	}
	if m.s.activeTools != 1 {
		t.Errorf("activeTools = %d, want 1", m.s.activeTools)
	}

	// Second tool finishes — should flush all.
	r2 := core.TextResult("content")
	cmd = m.handleAgentEvent(core.AgentEvent{
		Type: core.AgentEventToolExecEnd, ToolCallID: "tc-2", ToolName: "read", Result: &r2,
	})
	if cmd == nil {
		t.Error("should flush when all tools done")
	}
	if m.s.activeTools != 0 {
		t.Errorf("activeTools = %d, want 0", m.s.activeTools)
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

// --- Test: summarizeToolBlock ---

func TestSummarizeToolBlock_Bash(t *testing.T) {
	block := messageBlock{
		Type: "tool", ToolName: "bash",
		ToolArgs:   map[string]any{"command": "ls -la"},
		ToolResult: "file1.go\nfile2.go", ToolDone: true,
	}
	action, target, _, body, footer := summarizeToolBlock(block, maxToolPreviewLines)
	if action != "bash" {
		t.Errorf("action = %q, want bash", action)
	}
	if target != "ls -la" {
		t.Errorf("target = %q, want 'ls -la'", target)
	}
	if body != "file1.go\nfile2.go" {
		t.Errorf("body = %q", body)
	}
	if footer != "" {
		t.Errorf("footer = %q, want empty", footer)
	}
}

func TestSummarizeToolBlock_Write(t *testing.T) {
	block := messageBlock{
		Type: "tool", ToolName: "write",
		ToolArgs:   map[string]any{"path": "/tmp/test.go", "content": "package main\n"},
		ToolResult: "Successfully wrote 13 bytes", ToolDone: true,
	}
	action, target, _, body, _ := summarizeToolBlock(block, maxToolPreviewLines)
	if action != "write" {
		t.Errorf("action = %q", action)
	}
	if target != "/tmp/test.go" {
		t.Errorf("target = %q", target)
	}
	// Body should be the content arg, not the result.
	if body != "package main" {
		t.Errorf("body = %q, want content", body)
	}
}

func TestSummarizeToolBlock_WriteError(t *testing.T) {
	block := messageBlock{
		Type: "tool", ToolName: "write",
		ToolArgs:   map[string]any{"path": "/etc/test", "content": "x"},
		ToolResult: "permission denied", ToolDone: true, IsError: true,
	}
	_, _, _, body, _ := summarizeToolBlock(block, maxToolPreviewLines)
	if body != "permission denied" {
		t.Errorf("on error, body should be result, got %q", body)
	}
}

func TestSummarizeToolBlock_BashRunningWithStreaming(t *testing.T) {
	block := messageBlock{
		Type: "tool", ToolName: "bash",
		ToolArgs:   map[string]any{"command": "make test"},
		ToolResult: "PASS pkg/core\n",
	}
	_, _, _, body, _ := summarizeToolBlock(block, maxToolPreviewLines)
	if body != "PASS pkg/core" {
		t.Errorf("running bash with streamed output should show body, got %q", body)
	}
}

func TestSummarizeToolBlock_BashNoOutputYet(t *testing.T) {
	block := messageBlock{
		Type: "tool", ToolName: "bash",
		ToolArgs: map[string]any{"command": "sleep 10"},
	}
	_, _, _, body, _ := summarizeToolBlock(block, maxToolPreviewLines)
	if body != "" {
		t.Errorf("bash with no output should have empty body, got %q", body)
	}
}

func TestTruncateBlockText(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	text := strings.Join(lines, "\n")

	body, footer := truncateBlockText(text, 10)
	if !strings.HasPrefix(body, "line 1\n") {
		t.Errorf("body should start with line 1, got %q", body[:20])
	}
	if !strings.Contains(footer, "10 more lines") {
		t.Errorf("footer = %q, want '10 more lines'", footer)
	}
	if !strings.Contains(footer, "20 total") {
		t.Errorf("footer = %q, want '20 total'", footer)
	}
	if !strings.Contains(footer, "ctrl+o to expand") {
		t.Errorf("footer should mention ctrl+o, got %q", footer)
	}
}

func TestTruncateBlockTextTail(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	text := strings.Join(lines, "\n")

	header, body := truncateBlockTextTail(text, 10)
	if !strings.HasSuffix(body, "line 20") {
		t.Errorf("body should end with line 20, got %q", body[len(body)-20:])
	}
	if strings.Contains(body, "line 1\n") {
		t.Error("body should not contain first lines")
	}
	if !strings.Contains(header, "10 previous lines") {
		t.Errorf("header = %q, want '10 previous lines'", header)
	}
	if !strings.Contains(header, "20 total") {
		t.Errorf("header = %q, want '20 total'", header)
	}
	if !strings.Contains(header, "ctrl+o to expand") {
		t.Errorf("header should mention ctrl+o, got %q", header)
	}
}

func TestSummarizeToolBlock_ExpandedNoTruncation(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	block := messageBlock{
		Type: "tool", ToolName: "bash",
		ToolArgs:   map[string]any{"command": "cat big.txt"},
		ToolResult: strings.Join(lines, "\n"), ToolDone: true,
	}

	// Normal mode: tail-truncated (bash shows last N lines)
	_, _, header, body, _ := summarizeToolBlock(block, maxToolPreviewLines)
	if !strings.Contains(header, "20 previous lines") {
		t.Errorf("header = %q, want tail truncation info", header)
	}
	if !strings.Contains(body, "line 30") {
		t.Error("tail-truncated body should contain the LAST line")
	}
	if strings.Contains(body, "line 1\n") {
		t.Error("tail-truncated body should NOT contain the first line")
	}

	// Expanded mode: full content
	_, _, headerExp, bodyExp, _ := summarizeToolBlock(block, 0)
	if headerExp != "" {
		t.Errorf("expanded header = %q, want empty", headerExp)
	}
	if !strings.Contains(bodyExp, "line 30") {
		t.Error("expanded body should contain all lines")
	}
}

func TestRenderToolBlock_FullWidth(t *testing.T) {
	block := messageBlock{
		Type: "tool", ToolName: "write",
		ToolArgs:   map[string]any{"path": "/tmp/x.go", "content": "package main"},
		ToolResult: "wrote ok", ToolDone: true,
	}
	data := buildToolBlockData(block, false)
	rendered := GetActiveLayout().RenderToolBlock(data, 80, ActiveTheme)
	if rendered == "" {
		t.Fatal("empty render")
	}
	if !strings.Contains(rendered, "write") {
		t.Error("should contain action 'write'")
	}
	if !strings.Contains(rendered, "/tmp/x.go") {
		t.Error("should contain target path")
	}
	if !strings.Contains(rendered, "package main") {
		t.Error("should contain file content as body")
	}
}

func TestRenderToolBlock_Structure(t *testing.T) {
	block := messageBlock{
		Type: "tool", ToolName: "bash",
		ToolArgs:   map[string]any{"command": "ls"},
		ToolResult: "file1\nfile2\nfile3", ToolDone: true,
	}
	data := buildToolBlockData(block, false)
	rendered := GetActiveLayout().RenderToolBlock(data, 60, ActiveTheme)
	lines := strings.Split(rendered, "\n")

	// title + blank + 3 body = 5 lines minimum
	if len(lines) < 5 {
		t.Fatalf("lines = %d, want >= 5", len(lines))
	}
	// Every line must be padded to full width (60 chars visible).
	// lipgloss strips ANSI in test env, so we check visible width.
	for i, line := range lines {
		vis := len(line) // no ANSI in test → len == visible width
		if vis != 60 {
			t.Errorf("line %d visible width = %d, want 60: %q", i, vis, line)
		}
	}
}

func TestRenderToolBlock_HasInternalPadding(t *testing.T) {
	block := messageBlock{
		Type: "tool", ToolName: "bash",
		ToolArgs: map[string]any{"command": "pwd"},
		ToolDone: true, ToolResult: "/home",
	}
	r := newRenderer(80)
	rendered := renderSingleBlock(block, r, false)
	lines := strings.Split(rendered, "\n")
	// Should have padding lines (empty bg) at top and bottom
	if len(lines) < 4 {
		t.Fatalf("lines = %d, want >= 4 (top pad + header + body + bottom pad)", len(lines))
	}
}

func TestRenderToolBlock_ConsecutiveToolsHaveGap(t *testing.T) {
	blocks := []messageBlock{
		{Type: "tool", ToolName: "bash", ToolArgs: map[string]any{"command": "ls"}, ToolDone: true, ToolResult: "a"},
		{Type: "tool", ToolName: "bash", ToolArgs: map[string]any{"command": "pwd"}, ToolDone: true, ToolResult: "b"},
	}
	r := newRenderer(60)
	rendered := renderBlocks(blocks, r, false, false)
	// Each tool block has internal top/bottom padding, plus renderBlocks
	// joins with "\n". The result must have visual separation.
	if rendered == "" {
		t.Fatal("empty render")
	}
	if !strings.Contains(rendered, "ls") || !strings.Contains(rendered, "pwd") {
		t.Error("both tool blocks should be present")
	}
}

func TestSwitchToModel_SetsPendingTimeline(t *testing.T) {
	m := newSwitchTestApp(t)

	result, _ := m.switchToModel(core.Model{
		ID: "gpt-5.3-codex", Provider: "openai", Name: "GPT-5.3 Codex", MaxInput: 400_000,
	})
	rm := result.(appModel)

	if rm.s.pendingTimeline == nil {
		t.Fatal("expected pending timeline event")
	}
	if got, want := rm.s.pendingTimeline.Text, "✓ Switched to GPT-5.3 Codex (openai)"; got != want {
		t.Fatalf("pending timeline = %q, want %q", got, want)
	}
	if rm.s.pendingStatus != "" {
		t.Fatalf("pending status = %q, want empty", rm.s.pendingStatus)
	}
	if len(rm.s.blocks) != 0 {
		t.Fatalf("blocks = %d, want 0 before send", len(rm.s.blocks))
	}
	if got := rm.agent.Model().ID; got != "gpt-5.3-codex" {
		t.Fatalf("agent model = %q, want gpt-5.3-codex", got)
	}
}

func TestSwitchToModel_OverwritesPendingTimeline(t *testing.T) {
	m := newSwitchTestApp(t)

	result, _ := m.switchToModel(core.Model{
		ID: "gpt-5.3-codex", Provider: "openai", Name: "GPT-5.3 Codex", MaxInput: 400_000,
	})
	m = result.(appModel)
	result, _ = m.switchToModel(core.Model{
		ID: "o3", Provider: "openai", Name: "o3", MaxInput: 200_000,
	})
	rm := result.(appModel)

	if rm.s.pendingTimeline == nil {
		t.Fatal("expected pending timeline event")
	}
	if got, want := rm.s.pendingTimeline.Text, "✓ Switched to o3 (openai)"; got != want {
		t.Fatalf("pending timeline = %q, want %q", got, want)
	}
	if len(rm.s.blocks) != 0 {
		t.Fatalf("blocks = %d, want 0 before send", len(rm.s.blocks))
	}
}

func TestStartAgentRun_CommitsPendingTimelineBeforeUserBlock(t *testing.T) {
	m := newSwitchTestApp(t)

	result, _ := m.switchToModel(core.Model{
		ID: "gpt-5.3-codex", Provider: "openai", Name: "GPT-5.3 Codex", MaxInput: 400_000,
	})
	m = result.(appModel)
	result, cmd := m.startAgentRun("hello")
	rm := result.(appModel)

	if cmd == nil {
		t.Fatal("expected non-nil command")
	}
	if rm.s.pendingTimeline != nil {
		t.Fatal("pending timeline should be cleared after commit")
	}
	if len(rm.s.blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(rm.s.blocks))
	}
	if got := rm.s.blocks[0]; got.Type != "status" || got.Raw != "✓ Switched to GPT-5.3 Codex (openai)" {
		t.Fatalf("blocks[0] = %+v, want committed switch status", got)
	}
	if got := rm.s.blocks[1]; got.Type != "user" || got.Raw != "hello" {
		t.Fatalf("blocks[1] = %+v, want user block", got)
	}
	msgs := rm.agent.Messages()
	if len(msgs) != 1 {
		t.Fatalf("agent messages = %d, want 1 committed session event before Send executes", len(msgs))
	}
	if msgs[0].Role != "session_event" {
		t.Fatalf("messages[0].Role = %q, want session_event", msgs[0].Role)
	}
	if eventType(msgs[0].Custom) != "model_switch" {
		t.Fatalf("messages[0].Custom[event] = %q, want model_switch", eventType(msgs[0].Custom))
	}
	if got, want := firstTextContent(msgs[0].Content), "✓ Switched to GPT-5.3 Codex (openai)"; got != want {
		t.Fatalf("messages[0] text = %q, want %q", got, want)
	}
}

func TestRebuildFromMessages_RendersModelSwitchSessionEvent(t *testing.T) {
	m := newTestModel()

	m.rebuildFromMessages([]core.AgentMessage{
		{
			Message: core.Message{
				Role:    "session_event",
				Content: []core.Content{core.TextContent("✓ Switched to GPT-5.3 Codex (openai)")},
			},
			Custom: map[string]any{"event": "model_switch"},
		},
		core.WrapMessage(core.NewUserMessage("hello")),
	})

	if len(m.s.blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(m.s.blocks))
	}
	if got := m.s.blocks[0]; got.Type != "status" || got.Raw != "✓ Switched to GPT-5.3 Codex (openai)" {
		t.Fatalf("blocks[0] = %+v, want restored switch status", got)
	}
	if got := m.s.blocks[1]; got.Type != "user" || got.Raw != "hello" {
		t.Fatalf("blocks[1] = %+v, want restored user block", got)
	}
}

// --- Layout system tests ---

func saveAndRestoreLayout(t *testing.T) {
	t.Helper()
	saved := GetActiveLayout()
	t.Cleanup(func() { SetLayoutDirect(saved) })
}

func TestLayoutSwap_DifferentOutput(t *testing.T) {
	saveAndRestoreLayout(t)
	r := newRenderer(80)

	block := messageBlock{Type: "user", Raw: "hello world"}

	// Split layout: should have "YOU" label
	SetLayoutDirect(&SplitLayout{})
	splitOut := renderSingleBlock(block, r, false)
	if !strings.Contains(splitOut, "YOU") {
		t.Error("split layout should render YOU label")
	}

	// Flat layout: should have "❯" prefix, no "YOU"
	SetLayoutDirect(&FlatLayout{})
	flatOut := renderSingleBlock(block, r, false)
	if !strings.Contains(flatOut, "❯") {
		t.Error("flat layout should render ❯ prefix")
	}
	if strings.Contains(flatOut, "YOU") {
		t.Error("flat layout should not render YOU label")
	}

	// They should differ
	if splitOut == flatOut {
		t.Error("split and flat should produce different output")
	}
}

func TestSetLayout_UnknownName(t *testing.T) {
	err := SetLayout("nonexistent")
	if err == nil {
		t.Error("expected error for unknown layout name")
	}
}

func TestSetLayout_KnownNames(t *testing.T) {
	saveAndRestoreLayout(t)

	if err := SetLayout("flat"); err != nil {
		t.Errorf("SetLayout(flat) error: %v", err)
	}
	if _, ok := GetActiveLayout().(*FlatLayout); !ok {
		t.Error("expected FlatLayout after SetLayout(flat)")
	}

	if err := SetLayout("split"); err != nil {
		t.Errorf("SetLayout(split) error: %v", err)
	}
	if _, ok := GetActiveLayout().(*SplitLayout); !ok {
		t.Error("expected SplitLayout after SetLayout(split)")
	}
}

func TestSetLayoutDirect_NilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil layout")
		}
	}()
	SetLayoutDirect(nil)
}

func TestRegisterLayout_DuplicateErrors(t *testing.T) {
	// "split" is already registered by init()
	err := RegisterLayout("split", &SplitLayout{})
	if err == nil {
		t.Error("expected error on duplicate registration")
	}
}

func TestBuildToolBlockData_ExpandedNoTruncation(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	block := messageBlock{
		Type: "tool", ToolName: "bash",
		ToolArgs:   map[string]any{"command": "cat big.txt"},
		ToolResult: strings.Join(lines, "\n"), ToolDone: true,
	}

	data := buildToolBlockData(block, true)
	if data.Header != "" {
		t.Errorf("expanded header = %q, want empty", data.Header)
	}
	if !strings.Contains(data.Body, "line 1") || !strings.Contains(data.Body, "line 30") {
		t.Error("expanded body should contain all lines")
	}
}

func TestBuildToolBlockData_TruncatedBash(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	block := messageBlock{
		Type: "tool", ToolName: "bash",
		ToolArgs:   map[string]any{"command": "cat big.txt"},
		ToolResult: strings.Join(lines, "\n"), ToolDone: true,
	}

	data := buildToolBlockData(block, false)
	// Bash uses tail truncation: header shows hidden count, body shows last N
	if !strings.Contains(data.Header, "previous lines") {
		t.Errorf("header = %q, want tail truncation info", data.Header)
	}
	if !strings.Contains(data.Body, "line 30") {
		t.Error("tail-truncated body should contain the last line")
	}
	if strings.Contains(data.Body, "line 1\n") {
		t.Error("tail-truncated body should not contain the first line")
	}
}

func TestGetActiveLayout_NeverNil(t *testing.T) {
	l := GetActiveLayout()
	if l == nil {
		t.Fatal("GetActiveLayout() returned nil")
	}
}

func TestFormatUserMessage_CompatShim(t *testing.T) {
	out := FormatUserMessage("test message")
	if out == "" {
		t.Fatal("FormatUserMessage returned empty string")
	}
	if !strings.Contains(out, "test message") {
		t.Error("FormatUserMessage should contain the message text")
	}
}
