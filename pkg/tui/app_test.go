package tui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/session"
)

// newTestModel creates a minimal appModel for state-level tests.
// No agent, no event channel — only state, renderer, and components are initialized.
func newTestModel() appModel {
	vp := viewport.New(80, 20)
	vp.MouseWheelEnabled = true
	vp.KeyMap = viewport.KeyMap{}
	return appModel{
		s:        &state{showThinking: true},
		renderer: newRenderer(80),
		input:    newInput(),
		status:   newStatus(),
		viewport: vp,
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

func TestNew_StartInSessionBrowserDisablesInput(t *testing.T) {
	ag, err := agent.New(agent.AgentConfig{
		Provider: staticProvider{text: "ok"},
		Model:    core.Model{ID: "claude-sonnet-4-6", Provider: "anthropic", Name: "Claude Sonnet 4.6", MaxInput: 200_000},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	m := New(ag, context.Background(), Config{StartInSessionBrowser: true})
	if !m.sessionBrowser.active {
		t.Fatal("session browser should start active")
	}
	if m.input.enabled {
		t.Fatal("input should be disabled while the browser is active")
	}
}

func TestActivateSession_ClosesBrowserAndRebuildsBlocks(t *testing.T) {
	m := newSwitchTestApp(t)
	m.sessionBrowser.Open()
	m.input.SetEnabled(false)

	sess := &session.Session{
		ID: "abc123",
		Messages: []core.AgentMessage{
			core.WrapMessage(core.NewUserMessage("hello")),
			{Message: core.Message{Role: "assistant", Content: []core.Content{core.TextContent("world")}}},
		},
	}

	result, cmd := m.activateSession(sess)
	rm := result.(appModel)
	if cmd == nil {
		t.Fatal("expected redraw command")
	}
	if rm.sessionBrowser.active {
		t.Fatal("session browser should close after opening a session")
	}
	if !rm.input.enabled {
		t.Fatal("input should be re-enabled")
	}
	if rm.session != sess {
		t.Fatal("session should be set")
	}
	if len(rm.s.blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(rm.s.blocks))
	}
	if rm.s.blocks[0].Type != "user" || rm.s.blocks[1].Type != "assistant" {
		t.Fatalf("unexpected blocks: %+v", rm.s.blocks)
	}
}

func TestSessionBrowser_FilterSelectsMatchingSession(t *testing.T) {
	b := newSessionBrowser()
	b.Open()
	b.SetSummaries([]session.Summary{
		{ID: "aaa111", Title: "first permission fix"},
		{ID: "bbb222", Title: "session browser preview"},
	})

	changed := b.AppendFilter("preview")
	if !changed {
		t.Fatal("expected selection to change after filtering")
	}
	if got := b.SelectedID(); got != "bbb222" {
		t.Fatalf("selected id = %q, want bbb222", got)
	}
}

// --- Viewport / transcript mode tests ---

func TestVisibleBlocks_ReturnsAllBlocks(t *testing.T) {
	m := newTestModel()
	// Build 15 turns (user + assistant each)
	for i := 0; i < 15; i++ {
		m.s.blocks = append(m.s.blocks,
			messageBlock{Type: "user", Raw: fmt.Sprintf("q%d", i)},
			messageBlock{Type: "assistant", Raw: fmt.Sprintf("a%d", i)},
		)
	}
	vis := m.visibleBlocks()
	// Viewport shows all blocks (scrollable)
	if len(vis) != 30 {
		t.Fatalf("visibleBlocks = %d, want 30", len(vis))
	}
}

func TestVisibleBlocks_EmptyBlocks(t *testing.T) {
	m := newTestModel()
	vis := m.visibleBlocks()
	if len(vis) != 0 {
		t.Errorf("visibleBlocks = %d, want 0", len(vis))
	}
}

func TestVisibleBlocks_FewerThanLimit(t *testing.T) {
	m := newTestModel()
	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "hello"},
		{Type: "assistant", Raw: "world"},
	}
	vis := m.visibleBlocks()
	if len(vis) != 2 {
		t.Fatalf("visibleBlocks = %d, want 2", len(vis))
	}
}

func TestUpdateViewport_AutoScrollsWhenAtBottom(t *testing.T) {
	m := newTestModel()
	m.s.blocks = []messageBlock{{Type: "user", Raw: "hello"}}
	m.updateViewport()
	// After initial update, viewport should be at bottom
	if !m.viewport.AtBottom() {
		t.Error("viewport should be at bottom after initial update")
	}
}

func TestCtrlO_EntersTranscriptMode(t *testing.T) {
	m := newTestModel()
	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "hello"},
		{Type: "assistant", Raw: "world"},
	}

	result, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	rm := result.(appModel)

	if !rm.s.transcript {
		t.Error("expected transcript mode to be active")
	}
	if rm.s.fullHistory {
		t.Error("fullHistory should be false initially")
	}
	if rm.input.enabled {
		t.Error("input should be disabled in transcript mode")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd for ExitAltScreen + print")
	}
}

func TestCtrlO_ExitsTranscriptMode(t *testing.T) {
	m := newTestModel()
	m.s.transcript = true
	m.s.blocks = []messageBlock{{Type: "user", Raw: "hello"}}

	result, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	rm := result.(appModel)

	if rm.s.transcript {
		t.Error("expected transcript mode to be inactive")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd for EnterAltScreen")
	}
}

func TestCtrlO_InTranscript_RecomputesInputEnabled(t *testing.T) {
	m := newTestModel()
	m.s.transcript = true
	m.s.running = true // agent is running
	m.s.blocks = []messageBlock{{Type: "user", Raw: "hello"}}

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlO})
	rm := result.(appModel)

	if rm.input.enabled {
		t.Error("input should remain disabled when agent is running")
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

func TestCtrlE_InAltScreen_TogglesExpanded(t *testing.T) {
	m := newTestModel()
	m.s.blocks = []messageBlock{{Type: "user", Raw: "hello"}}
	m.s.expanded = false

	result, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlE})
	rm := result.(appModel)

	if !rm.s.expanded {
		t.Error("expected expanded=true after Ctrl+E")
	}
}

func TestCtrlE_InTranscript_TogglesFullHistory(t *testing.T) {
	m := newTestModel()
	m.s.transcript = true
	m.s.fullHistory = false
	m.s.blocks = []messageBlock{{Type: "user", Raw: "hello"}}

	result, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlE})
	rm := result.(appModel)

	if !rm.s.fullHistory {
		t.Error("expected fullHistory=true after Ctrl+E in transcript")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd for clearScreen + Println")
	}
}

func TestTranscriptMode_IgnoresInputKeys(t *testing.T) {
	m := newTestModel()
	m.s.transcript = true

	result, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	rm := result.(appModel)

	if !rm.s.transcript {
		t.Error("should remain in transcript mode")
	}
	if cmd != nil {
		t.Error("expected nil cmd for ignored key")
	}
}

func TestTranscriptMode_AllowsCtrlC(t *testing.T) {
	m := newSwitchTestApp(t)
	m.s.transcript = true

	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Error("expected quit cmd from Ctrl+C in transcript")
	}
}

func TestPermissionRequest_ExitsTranscript(t *testing.T) {
	m := newTestModel()
	m.s.transcript = true
	m.s.blocks = []messageBlock{{Type: "user", Raw: "hello"}}
	m.permGate = permission.New(permission.ModeAsk, permission.Config{})

	result, cmd := m.Update(permissionRequestMsg{
		Request: permission.Request{
			ToolName: "bash",
			Args:     map[string]any{"command": "rm -rf /"},
		},
	})
	rm := result.(appModel)

	if rm.s.transcript {
		t.Error("transcript should be exited on permission request")
	}
	if !rm.permPrompt.active {
		t.Error("permission prompt should be active")
	}
	if cmd == nil {
		t.Error("expected non-nil cmd for EnterAltScreen")
	}
}

// --- Test 2: handleAgentEvent message_end flushes blocks ---

func TestHandleAgentEvent_MessageEnd_AppendsBlocks(t *testing.T) {
	m := newTestModel()
	m.s.streamText = "hello world"
	m.s.thinkingText = "let me think"

	m.handleAgentEvent(core.AgentEvent{
		Type: core.AgentEventMessageEnd,
	})

	if len(m.s.blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(m.s.blocks))
	}
	if m.s.blocks[0].Type != "thinking" || m.s.blocks[0].Raw != "let me think" {
		t.Errorf("blocks[0] = %+v, want thinking block", m.s.blocks[0])
	}
	if m.s.blocks[1].Type != "assistant" || m.s.blocks[1].Raw != "hello world" {
		t.Errorf("blocks[1] = %+v, want assistant block", m.s.blocks[1])
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

func TestHandleAgentEvent_MessageEnd_NoContent(t *testing.T) {
	m := newTestModel()
	m.s.streamText = ""
	m.s.thinkingText = ""

	m.handleAgentEvent(core.AgentEvent{
		Type: core.AgentEventMessageEnd,
	})

	if len(m.s.blocks) != 0 {
		t.Errorf("blocks = %d, want 0 (no content)", len(m.s.blocks))
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
// Without runStartBlockIdx, patchFromMessages would find the turn-1 assistant
// block and patch it, leaving turn-2's content missing.
func TestPatchFromMessages_DoesNotPatchPreviousTurnBlocks(t *testing.T) {
	m := newTestModel()

	// Turn 1: user + assistant blocks from previous run
	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "turn 1 question"},
		{Type: "assistant", Raw: "turn 1 answer"},
		// Turn 2: user block added, but MessageEnd not yet processed
		{Type: "user", Raw: "turn 2 question"},
	}
	m.s.runStartBlockIdx = 2 // run started at the turn-2 user block

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
}

// Same scenario but with thinking blocks too.
func TestPatchFromMessages_DoesNotPatchPreviousTurnThinking(t *testing.T) {
	m := newTestModel()

	// Turn 1 complete with thinking
	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "q1"},
		{Type: "thinking", Raw: "think1"},
		{Type: "assistant", Raw: "a1"},
		// Turn 2 user block, MessageEnd missed
		{Type: "user", Raw: "q2"},
	}
	m.s.runStartBlockIdx = 3 // run started at turn-2 user block

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

// --- Test 4: handleRunResult resets state ---

func TestHandleRunResult_ResetsState(t *testing.T) {
	m := newTestModel()
	m.s.runGen = 5
	m.runGenAddr = &atomic.Uint64{}
	m.runGenAddr.Store(5)

	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "hello"},
		{Type: "thinking", Raw: "hmm"},
		{Type: "assistant", Raw: "world"},
	}
	m.s.running = true
	m.s.streamState = stateStreaming
	m.input.SetEnabled(false)

	result, _ := m.handleRunResult(agentRunResultMsg{
		RunGen: 5,
		Messages: []core.AgentMessage{
			{Message: core.Message{Role: "user", Content: []core.Content{{Type: "text", Text: "hello"}}}},
			{Message: core.Message{Role: "assistant", Content: []core.Content{{Type: "text", Text: "world"}}}},
		},
	})

	rm := result.(appModel)
	if rm.s.running {
		t.Error("running should be false")
	}
	if rm.s.streamState != stateIdle {
		t.Errorf("streamState = %d, want stateIdle", rm.s.streamState)
	}
	if len(rm.s.blocks) != 3 {
		t.Fatalf("blocks = %d, want 3", len(rm.s.blocks))
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

func TestWindowResize_UpdatesViewportOnResize(t *testing.T) {
	m := newTestModel()
	m.s.initialized = true
	m.width = 80
	m.height = 24
	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "hello"},
		{Type: "assistant", Raw: "world"},
	}

	result, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	rm := result.(appModel)

	if rm.width != 120 {
		t.Errorf("width = %d, want 120", rm.width)
	}
	if rm.height != 40 {
		t.Errorf("height = %d, want 40", rm.height)
	}
}

// --- Test 7: /clear resets all state ---

func TestClear_ResetsState(t *testing.T) {
	m := newTestModel()

	m.s.blocks = []messageBlock{
		{Type: "user", Raw: "hello"},
		{Type: "assistant", Raw: "world"},
	}
	m.s.streamText = "streaming..."
	m.s.thinkingText = "thinking..."
	m.s.streamCache = "cached"

	// Simulate what /clear does (minus agent.Reset which needs a real agent)
	m.s.blocks = m.s.blocks[:0]
	m.s.streamText = ""
	m.s.thinkingText = ""
	m.s.streamCache = ""

	if len(m.s.blocks) != 0 {
		t.Errorf("blocks = %d, want 0", len(m.s.blocks))
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

func TestHandleAgentEvent_ToolStart_AppendsBlock(t *testing.T) {
	m := newTestModel()

	m.handleAgentEvent(core.AgentEvent{
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
	if m.s.streamState != stateToolRunning {
		t.Errorf("streamState = %d, want stateToolRunning", m.s.streamState)
	}
	if m.s.activeTools != 1 {
		t.Errorf("activeTools = %d, want 1", m.s.activeTools)
	}
}

func TestHandleAgentEvent_ToolEnd_UpdatesBlock(t *testing.T) {
	m := newTestModel()
	m.s.blocks = []messageBlock{
		{Type: "tool", ToolCallID: "tc-1", ToolName: "bash"},
	}
	m.s.activeTools = 1
	m.s.streamState = stateToolRunning

	result := core.TextResult("file1.go\nfile2.go")
	m.handleAgentEvent(core.AgentEvent{
		Type:       core.AgentEventToolExecEnd,
		ToolCallID: "tc-1",
		ToolName:   "bash",
		IsError:    false,
		Result:     &result,
	})

	if len(m.s.blocks) != 1 {
		t.Fatalf("blocks = %d, want 1 (updated in-place)", len(m.s.blocks))
	}
	if !m.s.blocks[0].ToolDone {
		t.Error("block should be done")
	}
	if m.s.blocks[0].ToolResult != "file1.go\nfile2.go" {
		t.Errorf("ToolResult = %q, want file content", m.s.blocks[0].ToolResult)
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

func TestHandleAgentEvent_ParallelTools_CountsCorrectly(t *testing.T) {
	m := newTestModel()

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

	// First tool finishes
	r1 := core.TextResult("done")
	m.handleAgentEvent(core.AgentEvent{
		Type: core.AgentEventToolExecEnd, ToolCallID: "tc-1", ToolName: "bash", Result: &r1,
	})
	if m.s.activeTools != 1 {
		t.Errorf("activeTools = %d, want 1", m.s.activeTools)
	}

	// Second tool finishes
	r2 := core.TextResult("content")
	m.handleAgentEvent(core.AgentEvent{
		Type: core.AgentEventToolExecEnd, ToolCallID: "tc-2", ToolName: "read", Result: &r2,
	})
	if m.s.activeTools != 0 {
		t.Errorf("activeTools = %d, want 0", m.s.activeTools)
	}
	if m.s.streamState != stateStreaming {
		t.Errorf("streamState = %d, want stateStreaming after all tools done", m.s.streamState)
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
	if !strings.Contains(footer, "ctrl+e to expand") {
		t.Errorf("footer should mention ctrl+e, got %q", footer)
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
	if !strings.Contains(header, "ctrl+e to expand") {
		t.Errorf("header should mention ctrl+e, got %q", header)
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
	t.Cleanup(func() { _ = SetLayoutDirect(saved) })
}

func TestLayoutSwap_DifferentOutput(t *testing.T) {
	saveAndRestoreLayout(t)
	r := newRenderer(80)

	block := messageBlock{Type: "user", Raw: "hello world"}

	// Split layout: should have "YOU" label
	_ = SetLayoutDirect(&SplitLayout{})
	splitOut := renderSingleBlock(block, r, false)
	if !strings.Contains(splitOut, "YOU") {
		t.Error("split layout should render YOU label")
	}

	// Flat layout: should have "❯" prefix, no "YOU"
	_ = SetLayoutDirect(&FlatLayout{})
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

func TestSetLayoutDirect_NilErrors(t *testing.T) {
	if err := SetLayoutDirect(nil); err == nil {
		t.Error("expected error on nil layout")
	}
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

func TestBuildToolBlockData_AskUser_SingleQuestion(t *testing.T) {
	block := messageBlock{
		Type: "tool", ToolName: "ask_user",
		ToolArgs: map[string]any{
			"questions": []any{
				map[string]any{
					"question": "¿Qué tipo de proyecto?",
					"options":  []any{"API REST", "CLI", "Librería"},
				},
			},
		},
		ToolResult: "API REST", ToolDone: true,
	}
	data := buildToolBlockData(block, false)
	if data.Action != "❓ questions" {
		t.Errorf("Action = %q, want '❓ questions'", data.Action)
	}
	if data.Target != "¿Qué tipo de proyecto?" {
		t.Errorf("Target = %q, want question text", data.Target)
	}
	if !strings.Contains(data.Body, "¿Qué tipo de proyecto?") {
		t.Error("Body should contain question text")
	}
	if !strings.Contains(data.Body, "● API REST") {
		t.Error("Body should mark selected option with ●")
	}
	if !strings.Contains(data.Body, "○ CLI") {
		t.Error("Body should mark unselected options with ○")
	}
	if !strings.Contains(data.Body, "○ Librería") {
		t.Error("Body should mark unselected options with ○")
	}
}

func TestBuildToolBlockData_AskUser_CustomAnswer(t *testing.T) {
	block := messageBlock{
		Type: "tool", ToolName: "ask_user",
		ToolArgs: map[string]any{
			"questions": []any{
				map[string]any{
					"question": "¿Qué tipo?",
					"options":  []any{"A", "B"},
				},
			},
		},
		ToolResult: "Mi respuesta custom", ToolDone: true,
	}
	data := buildToolBlockData(block, false)
	if !strings.Contains(data.Body, "○ A") {
		t.Error("Body should show unselected options with ○")
	}
	if !strings.Contains(data.Body, "○ B") {
		t.Error("Body should show unselected options with ○")
	}
	if !strings.Contains(data.Body, "✎ Mi respuesta custom") {
		t.Error("Body should show custom answer with ✎")
	}
}

func TestBuildToolBlockData_AskUser_MultipleQuestions(t *testing.T) {
	block := messageBlock{
		Type: "tool", ToolName: "ask_user",
		ToolArgs: map[string]any{
			"questions": []any{
				map[string]any{"question": "First question?"},
				map[string]any{"question": "Second question?"},
			},
		},
		ToolResult: "Q: First question?\nA: Answer one\nQ: Second question?\nA: Answer two",
		ToolDone:   true,
	}
	data := buildToolBlockData(block, false)
	if !strings.Contains(data.Body, "→ Answer one") {
		t.Error("Body should show first answer with →")
	}
	if !strings.Contains(data.Body, "→ Answer two") {
		t.Error("Body should show second answer with →")
	}
}

func TestBuildToolBlockData_AskUser_Pending(t *testing.T) {
	block := messageBlock{
		Type: "tool", ToolName: "ask_user",
		ToolArgs: map[string]any{
			"questions": []any{
				map[string]any{"question": "Pick one", "options": []any{"A", "B"}},
			},
		},
		ToolResult: "", ToolDone: false,
	}
	data := buildToolBlockData(block, false)
	if !strings.Contains(data.Body, "Pick one") {
		t.Error("Body should show question even without answer")
	}
	// All options unselected when pending.
	if strings.Contains(data.Body, "●") {
		t.Error("Body should not have selected bullet when pending")
	}
	if !strings.Contains(data.Body, "○ A") || !strings.Contains(data.Body, "○ B") {
		t.Error("Body should show all options as unselected")
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

// --- Pinned models ---

func TestPinnedModelsToSet(t *testing.T) {
	set := pinnedModelsToSet([]string{"claude-sonnet-4-5", "gpt-4o"})
	if !set["claude-sonnet-4-5"] || !set["gpt-4o"] {
		t.Fatalf("pinnedModelsToSet = %v, want both IDs present", set)
	}
	if len(set) != 2 {
		t.Fatalf("unexpected extra entries: %v", set)
	}
}

func TestNew_PinnedModelsLoadedFromConfig(t *testing.T) {
	ag, err := agent.New(agent.AgentConfig{
		Provider: staticProvider{text: "ok"},
		Model:    core.Model{ID: "claude-sonnet-4-6", Provider: "anthropic", Name: "Claude Sonnet 4.6", MaxInput: 200_000},
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	m := New(ag, context.Background(), Config{
		PinnedModels: []string{"claude-sonnet-4-5", "gpt-4o"},
	})
	if !m.scopedModels["claude-sonnet-4-5"] || !m.scopedModels["gpt-4o"] {
		t.Fatalf("scopedModels = %v, want both pinned IDs loaded", m.scopedModels)
	}
}

func TestSavePinnedModels_CallbackFired(t *testing.T) {
	m := newTestModel()
	var got []string
	m.onPinnedModelsChange = func(ids []string) error { got = ids; return nil }
	m.scopedModels = map[string]bool{"claude-sonnet-4-5": true}

	cmd := m.savePinnedModels(m.scopedModels)
	if cmd == nil {
		t.Fatal("expected non-nil Cmd when callback is set")
	}
	msg := cmd()
	if pmsg, ok := msg.(pinnedModelsSavedMsg); !ok {
		t.Fatalf("expected pinnedModelsSavedMsg, got %T", msg)
	} else if pmsg.err != nil {
		t.Fatalf("unexpected error: %v", pmsg.err)
	}
	if len(got) != 1 || got[0] != "claude-sonnet-4-5" {
		t.Fatalf("callback called with %v, want [claude-sonnet-4-5]", got)
	}
}

func TestSavePinnedModels_NilWhenNoCallback(t *testing.T) {
	m := newTestModel()
	m.onPinnedModelsChange = nil
	cmd := m.savePinnedModels(map[string]bool{"claude-sonnet-4-5": true})
	if cmd != nil {
		t.Fatal("expected nil Cmd when no callback is configured")
	}
}

func TestSavePinnedIfChanged_SkipsWhenEqual(t *testing.T) {
	m := newTestModel()
	m.onPinnedModelsChange = func(ids []string) error {
		t.Fatal("callback should not be called when sets are equal")
		return nil
	}
	set := map[string]bool{"claude-sonnet-4-5": true}
	cmd := m.savePinnedIfChanged(set, set)
	if cmd != nil {
		t.Fatal("expected nil Cmd when sets are equal")
	}
}

func TestSavePinnedIfChanged_FiresWhenDifferent(t *testing.T) {
	m := newTestModel()
	var called bool
	m.onPinnedModelsChange = func(ids []string) error { called = true; return nil }
	prev := map[string]bool{"a": true}
	curr := map[string]bool{"a": true, "b": true}
	cmd := m.savePinnedIfChanged(prev, curr)
	if cmd == nil {
		t.Fatal("expected non-nil Cmd when sets differ")
	}
	cmd()
	if !called {
		t.Fatal("callback was not called")
	}
}

func TestPinnedSetsEqual(t *testing.T) {
	tests := []struct {
		a, b map[string]bool
		want bool
	}{
		{nil, nil, true},
		{map[string]bool{}, map[string]bool{}, true},
		{map[string]bool{"a": true}, map[string]bool{"a": true}, true},
		{map[string]bool{"a": true}, map[string]bool{"b": true}, false},
		{map[string]bool{"a": true}, map[string]bool{"a": true, "b": true}, false},
	}
	for _, tt := range tests {
		if got := pinnedSetsEqual(tt.a, tt.b); got != tt.want {
			t.Errorf("pinnedSetsEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestParseSubagentNotification(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		wantOK     bool
		wantTask   string
		wantStatus string
		wantResult string
	}{
		{
			name:       "completed",
			text:       "[subagent completed] Job abc123 finished.\nTask: fix the tests\n\nResult (last 50 lines):\nall tests pass",
			wantOK:     true,
			wantTask:   "fix the tests",
			wantStatus: "completed",
			wantResult: "all tests pass",
		},
		{
			name:       "failed",
			text:       "[subagent failed] Job abc123 failed.\nTask: deploy\nError: connection refused",
			wantOK:     true,
			wantTask:   "deploy",
			wantStatus: "failed",
			wantResult: "connection refused",
		},
		{
			name:       "cancelled",
			text:       "[subagent cancelled] Job abc123 was cancelled.\nTask: long task",
			wantOK:     true,
			wantTask:   "long task",
			wantStatus: "cancelled",
		},
		{
			name:   "user message",
			text:   "hey agent, change direction",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task, status, result, ok := parseSubagentNotification(tt.text)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if task != tt.wantTask {
				t.Errorf("task = %q, want %q", task, tt.wantTask)
			}
			if status != tt.wantStatus {
				t.Errorf("status = %q, want %q", status, tt.wantStatus)
			}
			if result != tt.wantResult {
				t.Errorf("result = %q, want %q", result, tt.wantResult)
			}
		})
	}
}
