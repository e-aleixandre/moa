package tui

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/go-agent/pkg/agent"
	"github.com/ealeixandre/go-agent/pkg/core"
)

const renderInterval = 16 * time.Millisecond // ~60fps

// streamState tracks what the agent is doing.
type streamState int

const (
	stateIdle        streamState = iota
	stateStreaming               // between message_start → message_end
	stateToolRunning             // between tool_exec_start → tool_exec_end
)

// state holds mutable data that must not be copied by Bubble Tea's value semantics.
// strings.Builder, sync.Once, etc. are not safe to copy.
// All mutable conversational state lives here behind a pointer.
// Accessed only from the Bubble Tea goroutine (single-threaded).
type state struct {
	blocks       []messageBlock // conversation history, raw content
	flushedCount int            // blocks [0..flushedCount) are in terminal scrollback
	streamText   string         // current streaming assistant text
	thinkingText string         // current thinking text
	streamCache  string         // cached glamour render of streamText (updated by renderTick)
	dirty        bool           // streamText changed since last render tick
	running      bool           // agent is running (tick should continue)
	streamState  streamState
	showThinking bool      // toggle thinking visibility (Ctrl+T)
	initialized  bool      // first WindowSizeMsg processed (one-shot bottom push done)
	runGen       uint64    // incremented on each run; events from old runs are ignored
	cleanupOnce  sync.Once // idempotent cleanup
}

// taggedEvent pairs an agent event with the run generation it was produced in.
// Tagged at production time (in the subscriber callback), not at consumption time.
// This prevents late events from being misidentified as belonging to the current run.
type taggedEvent struct {
	event core.AgentEvent
	gen   uint64
}

// appModel is the root Bubble Tea model. It composes all components,
// routes messages, manages the agent event bridge, and owns conversation state.
type appModel struct {
	// Pointer to mutable state — safe across Bubble Tea model copies
	s *state

	// Dependencies (pointers/channels are reference types, safe to copy)
	agent      *agent.Agent
	renderer   *renderer
	eventCh    chan taggedEvent
	quit       chan struct{}
	unsub      func()
	baseCtx    context.Context // parent context for agent runs (carries signal cancellation)
	runGenAddr *atomic.Uint64  // shared with subscriber for production-time tagging

	// Components
	input  inputModel
	status statusModel

	// Layout
	width  int
	height int
}

// New creates the TUI model. The agent must already be configured.
// ctx is the parent context for agent runs (e.g., signal-aware context from main).
// Subscribes to agent events internally — the caller should NOT register
// their own subscriber when using TUI mode.
func New(ag *agent.Agent, ctx context.Context) appModel {
	eventCh := make(chan taggedEvent, 1024)
	quit := make(chan struct{})
	runGenAddr := &atomic.Uint64{} // shared with subscriber

	// Subscriber bridge: events are tagged with the run generation at PRODUCTION
	// time (when the subscriber receives them from the emitter). This prevents
	// late events from being misidentified as belonging to the current run.
	// Structural events never dropped, deltas are lossy.
	unsub := ag.Subscribe(func(e core.AgentEvent) {
		gen := runGenAddr.Load()
		tagged := taggedEvent{event: e, gen: gen}
		if isStructuralEvent(e) {
			select {
			case eventCh <- tagged:
			case <-time.After(5 * time.Second):
			}
			return
		}
		select {
		case eventCh <- tagged:
		default:
		}
	})

	return appModel{
		s:          &state{showThinking: true},
		agent:      ag,
		renderer:   newRenderer(80),
		eventCh:    eventCh,
		quit:       quit,
		unsub:      unsub,
		baseCtx:    ctx,
		runGenAddr: runGenAddr,
		input:      newInput(),
		status:     newStatus(),
	}
}

// isStructuralEvent returns true for events that must not be dropped.
// Only text/thinking deltas are lossy — everything else is structural.
func isStructuralEvent(e core.AgentEvent) bool {
	if e.Type == core.AgentEventMessageUpdate && e.AssistantEvent != nil {
		switch e.AssistantEvent.Type {
		case core.ProviderEventTextDelta, core.ProviderEventThinkingDelta:
			return false
		}
	}
	return true
}

// --- Bubble Tea interface ---

// Init returns initial commands: event listener.
// Cursor is static (no blink) so no BlinkCmd needed — zero idle CPU.
func (m appModel) Init() tea.Cmd {
	return m.waitForEvent()
}

// Update is the main message router.
func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.renderer.SetWidth(msg.Width)
		m.input.SetWidth(msg.Width)
		m.status.SetWidth(msg.Width)
		// Invalidate stream cache so next tick re-renders with new width
		if m.s.streamText != "" {
			m.s.dirty = true
		}
		// One-shot: on first WindowSizeMsg, push blank lines to scrollback
		// so the input appears at the bottom of the terminal.
		// These lines live in scrollback (via tea.Println), outside BT's
		// managed area — they don't interfere with future rendering.
		if !m.s.initialized {
			m.s.initialized = true
			// Input is ~5 lines (textarea 3 + border/padding). Leave room.
			padding := msg.Height - 5
			if padding > 0 {
				return m, tea.Println(strings.Repeat("\n", padding))
			}
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case agentEventMsg:
		// Ignore late events from previous runs
		var flushCmd tea.Cmd
		if msg.RunGen == m.s.runGen {
			flushCmd = m.handleAgentEvent(msg.Event)
		}
		return m, tea.Batch(flushCmd, m.waitForEvent())

	case agentDoneMsg:
		// Channel closed or quit signaled. Don't re-subscribe.
		return m, nil

	case agentRunResultMsg:
		return m.handleRunResult(msg)

	case renderTickMsg:
		if m.s.dirty {
			if m.s.streamText != "" {
				m.s.streamCache = m.renderer.RenderMarkdown(m.s.streamText)
			} else {
				m.s.streamCache = ""
			}
			m.s.dirty = false
		}
		// Tick runs while agent is running (not just stateStreaming),
		// so it survives tool_exec transitions.
		if m.s.running {
			return m, renderTick()
		}
		return m, nil

	case clearScreenDoneMsg:
		// Clear command finished, nothing to do
		return m, nil

	case clearThinkingStatusMsg:
		// Clear the ephemeral thinking toggle feedback
		if !m.s.running {
			m.status.SetText("")
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.status, cmd = m.status.Update(msg)
		return m, cmd
	}

	// Pass through to sub-components
	if m.s.streamState == stateIdle {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	return m, nil
}

// View renders the active zone only. Completed blocks are in terminal scrollback
// (flushed via tea.Println). View() shows only unflushed blocks + streaming + status + input.
// In expand mode, delegates to the full-screen pager.
func (m appModel) View() string {
	if m.width == 0 {
		return "Loading..."
	}
	var sections []string

	// Unflushed completed blocks (tool_start/tool_end during a run, before flush)
	for _, block := range m.s.blocks[m.s.flushedCount:] {
		if rendered := renderSingleBlock(block, m.renderer, m.s.showThinking); rendered != "" {
			sections = append(sections, rendered)
		}
	}

	// Streaming thinking (if visible and active)
	if m.s.thinkingText != "" && m.s.showThinking {
		wrapWidth := m.width - 2
		if wrapWidth < 20 {
			wrapWidth = 20
		}
		styled := thinkingStyle.Width(wrapWidth).PaddingLeft(2).Render(m.s.thinkingText)
		sections = append(sections, styled)
	}

	// Streaming assistant text (from cache, updated by renderTick)
	if m.s.streamCache != "" {
		sections = append(sections, m.s.streamCache)
	}

	// Status bar
	if sv := m.status.View(); sv != "" {
		sections = append(sections, sv)
	}

	// Input
	if iv := m.input.View(); iv != "" {
		sections = append(sections, iv)
	}

	if len(sections) == 0 {
		return m.input.View()
	}
	return strings.Join(sections, "\n")
}

// --- Key handling ---

// handleKey processes global shortcuts. All other keys propagate to
// the focused component (input when idle).
func (m appModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		if m.s.running {
			// Abort current run, stay in TUI
			m.agent.Abort()
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status", Raw: "(interrupted)",
			})
			return m, nil
		}
		// Idle → quit
		m.cleanup()
		return m, tea.Quit

	case tea.KeyCtrlD:
		m.cleanup()
		return m, tea.Quit

	case tea.KeyCtrlT:
		m.s.showThinking = !m.s.showThinking
		// Only affects: (1) unflushed blocks in View(), (2) streaming thinking, (3) Ctrl+O expand
		// Already-flushed scrollback is not modified (would require clear+reprint).
		// Show brief feedback so user knows the toggle state.
		if m.s.showThinking {
			m.status.SetText("thinking visible")
		} else {
			m.status.SetText("thinking hidden (new messages only)")
		}
		// Clear the status after a short delay unless the agent is running
		if !m.s.running {
			return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
				return clearThinkingStatusMsg{}
			})
		}
		return m, nil

	case tea.KeyCtrlO:
		// Reprint: clear screen+scrollback, then print all blocks cleanly.
		// Gives a fresh view with source-of-truth content and current thinking toggle.
		// tmux scroll works natively since blocks go to scrollback via tea.Println.
		if len(m.s.blocks) == 0 {
			return m, nil
		}
		content := renderBlocks(m.s.blocks, m.renderer, m.s.showThinking)
		return m, tea.Sequence(clearScreen(), tea.Println(content))

	case tea.KeyEnter:
		if m.s.running {
			return m, nil
		}
		text := m.input.Submit()
		if text == "" {
			return m, nil
		}
		if cmd, ok := ParseCommand(text); ok {
			return m.handleCommand(cmd)
		}
		return m.startAgentRun(text)
	}

	// All other keys: propagate to input when idle
	if m.s.streamState == stateIdle {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	return m, nil
}

// --- Flush logic ---

// flushBlocks renders blocks [from..to) and returns a single tea.Println Cmd.
// Joins all blocks into one string to guarantee ordering (single Println = single write).
func (m *appModel) flushBlocks(from, to int) tea.Cmd {
	if from >= to {
		return nil
	}
	var parts []string
	for i := from; i < to; i++ {
		rendered := renderSingleBlock(m.s.blocks[i], m.renderer, m.s.showThinking)
		if rendered != "" {
			parts = append(parts, rendered)
		}
	}
	m.s.flushedCount = to
	if len(parts) == 0 {
		return nil
	}
	return tea.Println(strings.Join(parts, "\n"))
}

// --- Agent interaction ---

// startAgentRun sends a prompt to the agent and starts streaming.
func (m appModel) startAgentRun(text string) (tea.Model, tea.Cmd) {
	m.s.blocks = append(m.s.blocks, messageBlock{Type: "user", Raw: text})

	// Flush user message to scrollback
	userFlush := m.flushBlocks(m.s.flushedCount, len(m.s.blocks))

	m.s.running = true
	m.s.runGen++
	m.runGenAddr.Store(m.s.runGen) // sync with subscriber for production-time tagging
	m.s.streamState = stateStreaming
	m.s.streamText = ""
	m.s.thinkingText = ""
	m.s.streamCache = ""
	m.input.SetEnabled(false)
	m.status.SetText("thinking...")

	agentRef := m.agent
	gen := m.s.runGen
	baseCtx := m.baseCtx

	// tea.Sequence guarantees: user message prints BEFORE agent starts.
	// renderTick and spinner can batch concurrently (no ordering need).
	return m, tea.Sequence(
		userFlush,
		tea.Batch(
			// Cmd 1: run agent (blocks until complete)
			func() tea.Msg {
				msgs, err := agentRef.Send(baseCtx, text)
				return agentRunResultMsg{Err: err, Messages: msgs, RunGen: gen}
			},
			// Cmd 2: start render tick
			renderTick(),
			// Cmd 3: spinner
			m.status.spinner.Tick,
		),
	)
}

// handleAgentEvent processes a single agent event, updating TUI state.
// Returns a tea.Cmd to flush blocks to scrollback (or nil).
func (m *appModel) handleAgentEvent(e core.AgentEvent) tea.Cmd {
	switch e.Type {
	case core.AgentEventMessageUpdate:
		if e.AssistantEvent == nil {
			return nil
		}
		switch e.AssistantEvent.Type {
		case core.ProviderEventTextDelta:
			m.s.streamText += e.AssistantEvent.Delta
			m.s.dirty = true
		case core.ProviderEventThinkingDelta:
			m.s.thinkingText += e.AssistantEvent.Delta
			m.s.dirty = true
		}
		return nil

	case core.AgentEventMessageStart:
		m.s.streamText = ""
		m.s.thinkingText = ""
		m.s.streamCache = ""
		m.s.streamState = stateStreaming
		m.status.SetText("thinking...")
		return nil

	case core.AgentEventMessageEnd:
		// Flush thinking first (persists above the response, dimmed)
		if m.s.thinkingText != "" {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "thinking", Raw: m.s.thinkingText,
			})
		}
		// Then flush assistant text
		if m.s.streamText != "" {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "assistant", Raw: m.s.streamText,
			})
		}
		m.s.streamText = ""
		m.s.thinkingText = ""
		m.s.streamCache = ""
		return m.flushBlocks(m.s.flushedCount, len(m.s.blocks))

	case core.AgentEventToolExecStart:
		m.s.streamState = stateToolRunning
		m.status.SetText("running " + e.ToolName + "...")
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "tool_start", ToolName: e.ToolName, ToolArgs: e.Args,
		})
		return m.flushBlocks(m.s.flushedCount, len(m.s.blocks))

	case core.AgentEventToolExecEnd:
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "tool_end", ToolName: e.ToolName, IsError: e.IsError,
		})
		m.s.streamState = stateStreaming
		m.status.SetText("thinking...")
		return m.flushBlocks(m.s.flushedCount, len(m.s.blocks))
	}

	return nil
}

// --- Reconciliation ---

// handleRunResult processes the final result from agent.Send().
func (m appModel) handleRunResult(msg agentRunResultMsg) (tea.Model, tea.Cmd) {
	// Ignore results from previous runs (e.g., aborted run finishing late)
	if msg.RunGen != m.s.runGen {
		return m, nil
	}
	// Bump generation so any late-arriving events from this run are ignored.
	m.s.runGen++
	m.runGenAddr.Store(m.s.runGen)

	// Patch: correct last assistant/thinking content from source-of-truth.
	// Does NOT rebuild blocks — preserves event-derived blocks (tool_start, etc.).
	m.patchFromMessages(msg.Messages)

	// Flush any blocks that weren't flushed by events (edge case: dropped events)
	flushCmd := m.flushBlocks(m.s.flushedCount, len(m.s.blocks))

	m.s.running = false
	m.s.streamState = stateIdle
	m.s.streamText = ""
	m.s.thinkingText = ""
	m.s.streamCache = ""
	m.status.SetText("")
	m.input.SetEnabled(true)

	if msg.Err != nil && msg.Err != context.Canceled {
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "error", Raw: "Error: " + msg.Err.Error(),
		})
		errorFlush := m.flushBlocks(m.s.flushedCount, len(m.s.blocks))
		return m, tea.Batch(flushCmd, errorFlush)
	}
	return m, flushCmd
}

// patchFromMessages corrects the last assistant/thinking block content from
// the source-of-truth messages. Does NOT rebuild — preserves event-derived blocks
// (tool_start with args, etc.) that messages don't contain.
//
// Also creates missing blocks: if agentRunResultMsg arrives before the
// AgentEventMessageEnd event is processed (async emitter race), the assistant/thinking
// blocks won't exist yet. In that case, append them so flushBlocks can print them.
func (m *appModel) patchFromMessages(msgs []core.AgentMessage) {
	if msgs == nil {
		return
	}
	// Extract the final assistant text from source-of-truth messages.
	var lastAssistantText string
	var lastThinkingText string
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" {
			for _, c := range msgs[i].Content {
				if c.Type == "text" && c.Text != "" {
					lastAssistantText = c.Text
				}
				if c.Type == "thinking" && c.Thinking != "" {
					lastThinkingText = c.Thinking
				}
			}
			break
		}
	}

	// Patch or create thinking block
	if lastThinkingText != "" {
		found := false
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			if m.s.blocks[i].Type == "thinking" {
				m.s.blocks[i].Raw = lastThinkingText
				found = true
				break
			}
		}
		if !found {
			// MessageEnd event wasn't processed yet — create the block
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "thinking", Raw: lastThinkingText,
			})
		}
	}

	// Patch or create assistant block
	if lastAssistantText != "" {
		found := false
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			if m.s.blocks[i].Type == "assistant" {
				m.s.blocks[i].Raw = lastAssistantText
				found = true
				break
			}
		}
		if !found {
			// MessageEnd event wasn't processed yet — create the block
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "assistant", Raw: lastAssistantText,
			})
		}
	}
}

// rebuildFromMessages reconstructs blocks from the agent's source-of-truth messages.
// Used only for initial recovery — normal flow uses patchFromMessages.
func (m *appModel) rebuildFromMessages(msgs []core.AgentMessage) {
	m.s.blocks = m.s.blocks[:0]
	for _, msg := range msgs {
		switch msg.Role {
		case "user":
			if len(msg.Content) > 0 {
				m.s.blocks = append(m.s.blocks, messageBlock{
					Type: "user", Raw: msg.Content[0].Text,
				})
			}
		case "assistant":
			for _, c := range msg.Content {
				switch {
				case c.Type == "thinking" && c.Thinking != "":
					m.s.blocks = append(m.s.blocks, messageBlock{
						Type: "thinking", Raw: c.Thinking,
					})
				case c.Type == "text" && c.Text != "":
					m.s.blocks = append(m.s.blocks, messageBlock{
						Type: "assistant", Raw: c.Text,
					})
				}
			}
		case "tool_result":
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "tool_end", ToolName: msg.ToolName, IsError: msg.IsError,
			})
		}
	}
}

// --- Commands ---

func (m appModel) handleCommand(cmd string) (tea.Model, tea.Cmd) {
	switch cmd {
	case "clear":
		if err := m.agent.Reset(); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: err.Error(),
			})
			return m, nil
		}
		m.s.blocks = m.s.blocks[:0]
		m.s.flushedCount = 0
		m.s.streamText = ""
		m.s.thinkingText = ""
		m.s.streamCache = ""
		// Clear screen + scrollback via the system clear command, then
		// tell BT to repaint. ExecProcess bypasses BT's renderer entirely
		// so escape sequences don't interfere with its internal state.
		// Falls back to ClearScreen if clear(1) isn't available.
		return m, tea.Sequence(
			clearScreen(),
			func() tea.Msg { return tea.ClearScreen() },
		)
	case "exit", "quit":
		m.cleanup()
		return m, tea.Quit
	default:
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "error", Raw: "Unknown command: /" + cmd,
		})
		return m, nil
	}
}

// --- Helpers ---

// waitForEvent returns a Cmd that blocks until the next agent event.
// The run generation comes FROM the tagged event (stamped at production time),
// not captured at Cmd creation time.
func (m appModel) waitForEvent() tea.Cmd {
	eventCh := m.eventCh
	quit := m.quit
	return func() tea.Msg {
		select {
		case tagged, ok := <-eventCh:
			if !ok {
				return agentDoneMsg{}
			}
			return agentEventMsg{Event: tagged.event, RunGen: tagged.gen}
		case <-quit:
			return agentDoneMsg{}
		}
	}
}

// renderTick returns a Cmd that fires after renderInterval (~60fps).
func renderTick() tea.Cmd {
	return tea.Tick(renderInterval, func(time.Time) tea.Msg {
		return renderTickMsg{}
	})
}

// cleanup releases resources. Idempotent — safe to call multiple times.
func (m *appModel) cleanup() {
	m.s.cleanupOnce.Do(func() {
		close(m.quit)
		if m.unsub != nil {
			m.unsub()
		}
		m.agent.Abort()
	})
}
