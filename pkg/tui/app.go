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
	blocks        []messageBlock // conversation history, raw content
	streamText    string         // current streaming assistant text
	thinkingText  string         // current thinking text
	dirty         bool           // streamText changed since last render tick
	running       bool           // agent is running (tick should continue)
	streamState   streamState
	showThinking  bool      // toggle thinking visibility (Ctrl+T)
	runGen        uint64    // incremented on each run; events from old runs are ignored
	cleanupOnce   sync.Once // idempotent cleanup
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
	conversation conversationModel
	input        inputModel
	status       statusModel

	// Layout
	width  int
	height int

	// Config (immutable after creation)
	liteMode bool // skip streaming render, show only completed messages
}

// Options configures the TUI.
type Options struct {
	LiteMode bool // skip streaming render to reduce CPU usage
}

// New creates the TUI model. The agent must already be configured.
// ctx is the parent context for agent runs (e.g., signal-aware context from main).
// Subscribes to agent events internally — the caller should NOT register
// their own subscriber when using TUI mode.
func New(ag *agent.Agent, ctx context.Context, opts Options) appModel {
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
		s:            &state{showThinking: true},
		agent:        ag,
		renderer:     newRenderer(80),
		eventCh:      eventCh,
		quit:         quit,
		unsub:        unsub,
		baseCtx:      ctx,
		runGenAddr:   runGenAddr,
		liteMode:     opts.LiteMode,
		conversation: newConversation(),
		input:        newInput(),
		status:       newStatus(),
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
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.renderer.SetWidth(msg.Width)
		// Layout: viewport gets all height minus input (3 lines) and status (1 line) and gap
		vpHeight := msg.Height - 5
		if vpHeight < 1 {
			vpHeight = 1
		}
		m.conversation.SetSize(msg.Width, vpHeight)
		m.input.SetWidth(msg.Width)
		m.status.SetWidth(msg.Width)
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case agentEventMsg:
		// Ignore late events from previous runs
		if msg.RunGen == m.s.runGen {
			m.handleAgentEvent(msg.Event)
		}
		return m, m.waitForEvent()

	case agentDoneMsg:
		// Channel closed or quit signaled. Don't re-subscribe.
		return m, nil

	case agentRunResultMsg:
		// Ignore results from previous runs (e.g., aborted run finishing late)
		if msg.RunGen != m.s.runGen {
			return m, nil
		}
		// Bump generation so any late-arriving events from this run are ignored.
		// Both the local gen and the atomic (shared with subscriber) must be bumped
		// so events still being enqueued by the subscriber are tagged as stale.
		m.s.runGen++
		m.runGenAddr.Store(m.s.runGen)
		// Rebuild UI from source-of-truth messages.
		m.reconcileFromMessages(msg.Messages)
		if msg.Err != nil && msg.Err != context.Canceled {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: "Error: " + msg.Err.Error(),
			})
		}
		m.s.running = false
		m.s.streamState = stateIdle
		m.s.streamText = ""
		m.s.thinkingText = ""
		m.status.SetText("")
		m.input.SetEnabled(true)
		m.refreshViewport()
		return m, nil

	case renderTickMsg:
		if m.s.dirty {
			m.refreshViewport()
			m.s.dirty = false
		}
		// Tick runs while agent is running (not just stateStreaming),
		// so it survives tool_exec transitions.
		if m.s.running {
			return m, renderTick()
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.status, cmd = m.status.Update(msg)
		return m, cmd
	}

	// Pass through to sub-components
	var cmd tea.Cmd
	if m.s.streamState == stateIdle {
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}
	// Always propagate to conversation for scroll (PgUp/PgDn/mouse)
	m.conversation, cmd = m.conversation.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// View renders the full TUI layout.
func (m appModel) View() string {
	if m.width == 0 {
		return "Loading..."
	}
	var sections []string
	sections = append(sections, m.conversation.View())
	if sv := m.status.View(); sv != "" {
		sections = append(sections, sv)
	}
	sections = append(sections, m.input.View())
	return strings.Join(sections, "\n")
}

// --- Key handling ---

// handleKey processes global shortcuts. All other keys propagate to
// the focused component (input when idle, conversation when running).
func (m appModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		if m.s.running {
			// Abort current run, stay in TUI
			m.agent.Abort()
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status", Raw: "(interrupted)",
			})
			m.refreshViewport()
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
		m.refreshViewport()
		return m, nil

	case tea.KeyEnter:
		if m.s.running {
			// While running: propagate to conversation for scroll
			var cmd tea.Cmd
			m.conversation, cmd = m.conversation.Update(msg)
			return m, cmd
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

	// All other keys: propagate to focused component
	var cmds []tea.Cmd
	if m.s.streamState == stateIdle {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	} else {
		// While running: keys go to conversation (scroll with arrows/pgup/pgdn)
		var cmd tea.Cmd
		m.conversation, cmd = m.conversation.Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// --- Agent interaction ---

// startAgentRun sends a prompt to the agent and starts streaming.
func (m appModel) startAgentRun(text string) (tea.Model, tea.Cmd) {
	m.s.blocks = append(m.s.blocks, messageBlock{Type: "user", Raw: text})
	m.s.running = true
	m.s.runGen++
	m.runGenAddr.Store(m.s.runGen) // sync with subscriber for production-time tagging
	m.s.streamState = stateStreaming
	m.s.streamText = ""
	m.s.thinkingText = ""
	m.input.SetEnabled(false)
	m.status.SetText("thinking...")
	m.refreshViewport()

	agentRef := m.agent
	gen := m.s.runGen
	baseCtx := m.baseCtx
	return m, tea.Batch(
		// Cmd 1: run agent (blocks until complete)
		func() tea.Msg {
			msgs, err := agentRef.Send(baseCtx, text)
			return agentRunResultMsg{Err: err, Messages: msgs, RunGen: gen}
		},
		// Cmd 2: start render tick
		renderTick(),
		// Cmd 3: spinner
		m.status.spinner.Tick,
	)
}

// handleAgentEvent processes a single agent event, updating TUI state.
func (m *appModel) handleAgentEvent(e core.AgentEvent) {
	switch e.Type {
	case core.AgentEventMessageUpdate:
		if e.AssistantEvent == nil {
			return
		}
		switch e.AssistantEvent.Type {
		case core.ProviderEventTextDelta:
			m.s.streamText += e.AssistantEvent.Delta
			m.s.dirty = true
		case core.ProviderEventThinkingDelta:
			m.s.thinkingText += e.AssistantEvent.Delta
			m.s.dirty = true
		}

	case core.AgentEventMessageStart:
		m.s.streamText = ""
		m.s.thinkingText = ""
		m.s.streamState = stateStreaming
		m.status.SetText("thinking...")

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
		m.s.dirty = true

	case core.AgentEventToolExecStart:
		m.s.streamState = stateToolRunning
		m.status.SetText("running " + e.ToolName + "...")
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "tool_start", ToolName: e.ToolName, ToolArgs: e.Args,
		})
		m.s.dirty = true

	case core.AgentEventToolExecEnd:
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "tool_end", ToolName: e.ToolName, IsError: e.IsError,
		})
		m.s.streamState = stateStreaming
		m.status.SetText("thinking...")
		m.s.dirty = true
	}
}

// --- Reconciliation ---

// reconcileFromMessages rebuilds UI state from the agent's source-of-truth messages.
// Always rebuilds unconditionally: streaming deltas are lossy by design,
// so the UI may have partial/truncated text. The final messages from Send()
// are the only reliable source of content.
func (m *appModel) reconcileFromMessages(msgs []core.AgentMessage) {
	if msgs == nil {
		return
	}
	m.rebuildFromMessages(msgs)
}

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

// --- Viewport ---

// refreshViewport re-renders all blocks + streaming text from raw content.
func (m *appModel) refreshViewport() {
	var content strings.Builder
	content.WriteString(renderBlocks(m.s.blocks, m.renderer, m.s.showThinking))

	if !m.liteMode {
		// Thinking (dim, word-wrapped, shown during streaming)
		if m.s.thinkingText != "" && m.s.showThinking {
			wrapWidth := m.width - 2
			if wrapWidth < 20 {
				wrapWidth = 20
			}
			styled := thinkingStyle.Width(wrapWidth).PaddingLeft(2).Render(m.s.thinkingText)
			content.WriteString(styled + "\n")
		}
		// Streaming text: apply glamour in real-time so inline markdown (backticks,
		// bold, etc.) renders during streaming, not just on completion.
		if m.s.streamText != "" {
			content.WriteString(m.renderer.RenderMarkdown(m.s.streamText))
		}
	}
	// In lite mode, streaming text is not rendered — the user sees only completed
	// blocks. The spinner in the status bar indicates the agent is working.

	m.conversation.SetContent(content.String())
}

// --- Commands ---

func (m appModel) handleCommand(cmd string) (tea.Model, tea.Cmd) {
	switch cmd {
	case "clear":
		if err := m.agent.Reset(); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: err.Error(),
			})
		} else {
			m.s.blocks = m.s.blocks[:0]
			m.s.streamText = ""
			m.s.thinkingText = ""
		}
		m.refreshViewport()
		return m, nil
	case "exit", "quit":
		m.cleanup()
		return m, tea.Quit
	default:
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "error", Raw: "Unknown command: /" + cmd,
		})
		m.refreshViewport()
		return m, nil
	}
}

// --- Helpers ---

// waitForEvent returns a Cmd that blocks until the next agent event.
// The run generation comes FROM the tagged event (stamped at production time),
// not captured at Cmd creation time. This is critical: if we captured gen here,
// late events read after a runGen bump would be mislabeled as current.
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
