package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/session"
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
	blocks              []messageBlock // conversation history, raw content
	flushedCount        int            // blocks confirmed in scrollback (hidden from View)
	flushScheduledCount int            // blocks scheduled for tea.Println (not yet confirmed)
	flushEpoch          int            // incremented on /clear to invalidate stale flushDoneMsg
	streamText          string         // current streaming assistant text
	thinkingText        string         // current thinking text
	streamCache         string         // cached glamour render of streamText (updated by renderTick)
	dirty               bool           // streamText changed since last render tick
	running             bool           // agent is running (tick should continue)
	streamState         streamState
	activeTools         int                   // number of tool calls currently executing
	showThinking        bool                  // toggle thinking visibility (Ctrl+T)
	expanded            bool                  // toggle expanded tool results (Ctrl+O)
	initialized         bool                  // first WindowSizeMsg processed (one-shot bottom push done)
	runGen              uint64                // incremented on each run; events from old runs are ignored
	cleanupOnce         sync.Once             // idempotent cleanup
	pendingStatus       string                // transient generic status shown in View(), never persisted
	pendingTimeline     *pendingTimelineEvent // live timeline event shown in View() until next send
	sessionCost         float64               // accumulated USD cost this session
	runStartMsgCount    int                   // message count at start of current run (for delta cost)
}

// taggedEvent pairs an agent event with the run generation it was produced in.
// Tagged at production time (in the subscriber callback), not at consumption time.
// This prevents late events from being misidentified as belonging to the current run.
type taggedEvent struct {
	event core.AgentEvent
	gen   uint64
}

type pendingTimelineEvent struct {
	Text    string
	Message core.AgentMessage
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
	input          inputModel
	status         statusModel
	picker         pickerModel
	cmdPalette     cmdPalette
	permPrompt     permissionPrompt
	sessionBrowser sessionBrowser
	topBar         *StatusLine
	bottomBar      *StatusLine

	// Session persistence
	sessionStore *session.Store   // nil if persistence is disabled
	session      *session.Session // current session (nil if no persistence)

	// Display
	modelName string

	// Provider switching
	providerFactory ProviderFactory
	scopedModels    map[string]bool // model IDs pinned for Ctrl+P cycling

	// Permissions
	permGate *permission.Gate

	// Layout
	width  int
	height int
}

// ProviderFactory creates a provider for a given model.
// It must not write directly to stdout/stderr because the TUI may call it
// while Bubble Tea owns the terminal.
type ProviderFactory func(model core.Model) (core.Provider, error)

// Config configures the TUI. All fields are optional.
type Config struct {
	SessionStore          *session.Store   // persistence backend (nil = no persistence)
	Session               *session.Session // session to resume (nil = fresh start)
	StartInSessionBrowser bool             // open the session browser before entering chat
	ModelName             string           // display name for the active model (shown on startup)
	ProviderFactory       ProviderFactory  // creates providers for /model switching (nil = switching disabled)
	PermissionGate        *permission.Gate // permission gate (nil = yolo, no prompts)
}

// New creates the TUI model. The agent must already be configured.
// ctx is the parent context for agent runs (e.g., signal-aware context from main).
// Subscribes to agent events internally — the caller should NOT register
// their own subscriber when using TUI mode.
func New(ag *agent.Agent, ctx context.Context, cfg Config) appModel {
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

	m := appModel{
		s:               &state{showThinking: true},
		agent:           ag,
		renderer:        newRenderer(80),
		eventCh:         eventCh,
		quit:            quit,
		unsub:           unsub,
		baseCtx:         ctx,
		runGenAddr:      runGenAddr,
		input:           newInput(),
		status:          newStatus(),
		picker:          newPicker(),
		sessionBrowser:  newSessionBrowser(),
		topBar:          NewStatusLine(statusLineStyle),
		bottomBar:       NewStatusLine(statusLineStyle),
		sessionStore:    cfg.SessionStore,
		session:         cfg.Session,
		modelName:       cfg.ModelName,
		providerFactory: cfg.ProviderFactory,
		scopedModels:    make(map[string]bool),
		permGate:        cfg.PermissionGate,
	}

	// Initialize status line segments.
	if cfg.ModelName != "" {
		m.topBar.UpdateModelSegment(cfg.ModelName)
	}
	m.topBar.UpdateThinkingSegment(ag.ThinkingLevel())
	if m.permGate != nil {
		m.topBar.UpdatePermissionsSegment(string(m.permGate.Mode()))
	} else {
		m.topBar.UpdatePermissionsSegment("yolo")
	}
	m.topBar.UpdateContextSegment(0)
	if cfg.StartInSessionBrowser {
		m.sessionBrowser.Open()
		m.input.SetEnabled(false)
	}

	return m
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
	cmds := []tea.Cmd{m.waitForEvent()}
	if m.permGate != nil {
		cmds = append(cmds, m.waitForPermission())
	}
	if m.sessionBrowser.active {
		cmds = append(cmds, m.loadSessionBrowser())
	}
	return tea.Batch(cmds...)
}

// waitForPermission listens for the next permission request from the gate.
func (m appModel) waitForPermission() tea.Cmd {
	gate := m.permGate
	ctx := m.baseCtx
	return func() tea.Msg {
		select {
		case req, ok := <-gate.Requests():
			if !ok {
				return nil
			}
			return permissionRequestMsg{Request: req}
		case <-ctx.Done():
			return nil
		}
	}
}

// Update is the main message router.
func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		sizeChanged := m.width != msg.Width || m.height != msg.Height
		m.width = msg.Width
		m.height = msg.Height
		m.renderer.SetWidth(msg.Width)
		m.input.SetWidth(msg.Width)
		m.status.SetWidth(msg.Width)
		// Invalidate stream cache so next tick re-renders with new width.
		// The live area is still owned by View(); only scrollback needs explicit repaint.
		if m.s.streamText != "" && sizeChanged {
			m.s.dirty = true
		}
		// One-shot initialization on first WindowSizeMsg (renderer width is now set).
		if !m.s.initialized {
			m.s.initialized = true

			if m.session != nil && len(m.session.Messages) > 0 {
				m.rebuildFromMessages(m.session.Messages)
				m.refreshContextSegment()
			}

			if len(m.s.blocks) > 0 {
				content := renderBlocks(m.s.blocks, m.renderer, m.s.showThinking, false)
				m.s.flushedCount = len(m.s.blocks)
				m.s.flushScheduledCount = len(m.s.blocks)
				return m, tea.Println(content)
			}
			return m, nil
		}
		// Repaint scrollback only on actual terminal resize, not on synthetic
		// WindowSizeMsg from tea.Exec/forceRepaint (which would cause a loop).
		if !sizeChanged || m.sessionBrowser.active || m.s.flushedCount == 0 {
			return m, nil
		}
		content := renderBlocks(m.s.blocks[:m.s.flushedCount], m.renderer, m.s.showThinking, m.s.expanded)
		return m, tea.Sequence(clearScreen(), tea.Println(content))

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

	case flushDoneMsg:
		// Confirm blocks are in scrollback — safe to hide from View().
		// Ignore stale messages from before /clear.
		if msg.epoch == m.s.flushEpoch && msg.upTo > m.s.flushedCount {
			if msg.upTo > len(m.s.blocks) {
				msg.upTo = len(m.s.blocks)
			}
			m.s.flushedCount = msg.upTo
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

	case compactResultMsg:
		m.s.running = false
		m.input.SetEnabled(true)
		m.status.SetText("")
		if msg.Err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: "Compaction failed: " + msg.Err.Error(),
			})
		} else if msg.Payload == nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status", Raw: "Nothing to compact",
			})
		} else {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status",
				Raw:  fmt.Sprintf("✂ Context compacted (%dK → %dK tokens)", msg.Payload.TokensBefore/1000, msg.Payload.TokensAfter/1000),
			})
		}
		m.refreshContextSegment()
		cmds := []tea.Cmd{m.flushBlocks(len(m.s.blocks))}
		if m.agent != nil {
			cmds = append(cmds, m.saveSession(m.agent.Messages()))
		}
		return m, tea.Batch(cmds...)

	case permissionRequestMsg:
		mode := permission.ModeAsk
		if m.permGate != nil {
			mode = m.permGate.Mode()
		}
		m.permPrompt.Show(msg.Request, mode)
		return m, nil

	case sessionBrowserLoadedMsg:
		m.sessionBrowser.SetLoadError(msg.Err)
		if msg.Err != nil {
			return m, nil
		}
		m.sessionBrowser.SetSummaries(msg.Summaries)
		if id := m.sessionBrowser.SelectedID(); id != "" {
			return m, m.loadSessionPreview(id)
		}
		return m, nil

	case sessionPreviewLoadedMsg:
		if !m.sessionBrowser.active || msg.ID != m.sessionBrowser.SelectedID() {
			return m, nil
		}
		m.sessionBrowser.SetPreview(msg.Session, msg.Err)
		return m, nil

	case sessionOpenLoadedMsg:
		if msg.Err != nil {
			m.sessionBrowser.previewErr = msg.Err.Error()
			return m, nil
		}
		return m.activateSession(msg.Session)

	case sessionSavedMsg:
		// Session saved asynchronously. Log errors but don't interrupt the user.
		if msg.err != nil {
			// TODO: consider a subtle status indicator for save failures
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
	if m.sessionBrowser.active {
		return m.sessionBrowser.View(m.width, m.height)
	}

	// Content blocks — joined with "\n\n" (one blank line between blocks).
	var content []string

	for _, block := range m.s.blocks[m.s.flushedCount:] {
		if rendered := renderSingleBlock(block, m.renderer, m.s.showThinking); rendered != "" {
			content = append(content, rendered)
		}
	}

	// Streaming thinking (if visible and active)
	if m.s.thinkingText != "" && m.s.showThinking {
		content = append(content, GetActiveLayout().RenderThinking(m.s.thinkingText, m.width, ActiveTheme))
	}

	// Streaming assistant text (from cache, updated by renderTick)
	if m.s.streamCache != "" {
		content = append(content, GetActiveLayout().RenderAssistantText(m.s.streamCache, m.width))
	}

	// Status bar (spinner)
	if sv := m.status.View(); sv != "" {
		content = append(content, sv)
	}

	// Pending status (transient generic feedback — shown until next message send)
	l := GetActiveLayout()
	if m.s.pendingStatus != "" {
		content = append(content, l.RenderLiveNotice(m.s.pendingStatus, m.width, ActiveTheme))
	}
	if m.s.pendingTimeline != nil {
		content = append(content, l.RenderLiveNotice(m.s.pendingTimeline.Text, m.width, ActiveTheme))
	}

	// UI chrome — joined with "\n" (no extra blank lines).
	var chrome []string

	if tv := m.topBar.View(m.width); tv != "" {
		chrome = append(chrome, tv)
	}
	if m.permPrompt.active {
		// Permission prompt replaces the input area entirely
		if pv := m.permPrompt.View(m.width, ActiveTheme); pv != "" {
			chrome = append(chrome, pv)
		}
	} else if m.picker.active {
		if pv := m.picker.View(m.width); pv != "" {
			chrome = append(chrome, pv)
		}
	} else {
		if iv := m.input.View(); iv != "" {
			chrome = append(chrome, iv)
		}
	}
	if bv := m.bottomBar.View(m.width); bv != "" {
		chrome = append(chrome, bv)
	}
	// Command palette below everything (fixed height, no layout shift)
	if pv := m.cmdPalette.View(m.width, ActiveTheme); pv != "" {
		chrome = append(chrome, pv)
	}

	// Assemble: content (with blank-line gaps) + chrome (tight)
	var final []string
	if len(content) > 0 {
		contentStr := strings.Join(content, "\n\n")
		if m.s.flushedCount > 0 {
			contentStr = "\n" + contentStr // blank line gap from scrollback
		}
		final = append(final, contentStr)
	}
	if len(chrome) > 0 {
		final = append(final, strings.Join(chrome, "\n"))
	}

	if len(final) == 0 {
		return m.input.View()
	}
	return strings.Join(final, "\n\n")
}

// --- Key handling ---

// handleKey processes global shortcuts. All other keys propagate to
// the focused component (input when idle).
func (m appModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sessionBrowser.active {
		return m.handleSessionBrowserKey(msg)
	}

	// Permission prompt: intercept all keys.
	if m.permPrompt.active {
		return m.handlePermissionKey(msg)
	}

	// Picker mode: intercept all keys.
	if m.picker.active {
		return m.handlePickerKey(msg)
	}

	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		if m.cmdPalette.active {
			m.cmdPalette.Close()
			m.input.textarea.Reset()
			return m, m.forceRepaint()
		}
		if m.s.running {
			m.agent.Abort()
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status", Raw: "(interrupted)",
			})
			return m, nil
		}
		if msg.Type == tea.KeyCtrlC {
			m.cleanup()
			return m, tea.Quit
		}
		return m, nil

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

	case tea.KeyShiftTab:
		if m.s.running {
			return m, nil
		}
		level := cycleThinkingLevel(m.agent.ThinkingLevel())
		model := m.agent.Model()
		if err := m.agent.Reconfigure(nil, model, level); err != nil {
			return m, nil
		}
		m.topBar.UpdateThinkingSegment(level)
		m.status.SetText("thinking: " + level)
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
			return clearThinkingStatusMsg{}
		})

	case tea.KeyCtrlP:
		if m.s.running {
			return m, nil
		}
		return m.cycleScopedModel()

	case tea.KeyCtrlY:
		// Rotate permission mode: yolo → ask → auto → yolo
		if m.s.running {
			return m, nil
		}
		var next permission.Mode
		if m.permGate == nil {
			next = permission.ModeAsk
		} else {
			switch m.permGate.Mode() {
			case permission.ModeAsk:
				next = permission.ModeAuto
			case permission.ModeAuto:
				next = permission.ModeYolo
			default:
				next = permission.ModeAsk
			}
		}
		return m.handlePermissionsSwitch(string(next))

	case tea.KeyCtrlO:
		// Toggle expanded view: clear screen+scrollback, reprint all blocks.
		// First press expands tool results in full; second press collapses back.
		if len(m.s.blocks) == 0 {
			return m, nil
		}
		m.s.expanded = !m.s.expanded
		content := renderBlocks(m.s.blocks, m.renderer, m.s.showThinking, m.s.expanded)
		return m, tea.Sequence(clearScreen(), tea.Println(content))

	case tea.KeyEnter:
		if msg.Alt {
			// Option/Alt+Enter → pass to textarea for newline insertion
			break
		}
		if m.s.running {
			return m, nil
		}

		// Command palette: accept selected command
		if m.cmdPalette.active {
			selected := m.cmdPalette.Selected()
			m.cmdPalette.Close()
			if selected != "" {
				// Find command def to check if it takes args
				hasArgs := false
				for _, cmd := range allCommands {
					if cmd.Name == selected && cmd.Args != "" {
						hasArgs = true
						break
					}
				}
				if hasArgs {
					m.input.textarea.Reset()
					m.input.textarea.SetValue("/" + selected + " ")
					m.input.textarea.CursorEnd()
					return m, m.forceRepaint()
				}
				return m.handleCommand(selected)
			}
			return m, m.forceRepaint()
		}

		text := m.input.Submit()
		if text == "" {
			return m, nil
		}
		if cmd, ok := ParseCommand(text); ok {
			return m.handleCommand(cmd)
		}
		return m.startAgentRun(text)

	case tea.KeyTab:
		// Tab also accepts palette selection
		if m.cmdPalette.active {
			selected := m.cmdPalette.Selected()
			m.cmdPalette.Close()
			if selected != "" {
				m.input.textarea.Reset()
				m.input.textarea.SetValue("/" + selected + " ")
				m.input.textarea.CursorEnd()
			}
			return m, m.forceRepaint()
		}

	case tea.KeyUp:
		if m.cmdPalette.active {
			m.cmdPalette.MoveUp()
			return m, nil
		}

	case tea.KeyDown:
		if m.cmdPalette.active {
			m.cmdPalette.MoveDown()
			return m, nil
		}

	}

	// All other keys: propagate to input when idle, then update palette
	if m.s.streamState == stateIdle {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		// Update command palette based on current input text
		m.cmdPalette.Update(m.input.textarea.Value())
		return m, cmd
	}

	return m, nil
}

// --- Flush logic ---

// flushBlocks schedules blocks up to index `to` for printing to scrollback.
// Uses two counters to prevent the visual flash:
//   - flushScheduledCount: advanced immediately (prevents double-scheduling)
//   - flushedCount: advanced only after tea.Println executes (via flushDoneMsg)
//
// View() uses flushedCount, so blocks stay visible until the print is confirmed.
func (m *appModel) flushBlocks(to int) tea.Cmd {
	from := m.s.flushScheduledCount
	if from < m.s.flushedCount {
		from = m.s.flushedCount
	}
	if to <= from {
		return nil
	}

	var parts []string
	for i := from; i < to; i++ {
		rendered := renderSingleBlock(m.s.blocks[i], m.renderer, m.s.showThinking)
		if rendered != "" {
			parts = append(parts, rendered)
		}
	}
	m.s.flushScheduledCount = to
	epoch := m.s.flushEpoch

	done := func() tea.Msg { return flushDoneMsg{upTo: to, epoch: epoch} }

	if len(parts) == 0 {
		// Nothing to print, but still confirm the advance
		return done
	}
	body := strings.TrimRight(strings.Join(parts, "\n\n"), "\n")
	if from > 0 {
		body = "\n" + body // blank line gap from previous flush batch
	}
	return tea.Sequence(tea.Println(body), done)
}

// --- Agent interaction ---

// startAgentRun sends a prompt to the agent and starts streaming.
func (m appModel) startAgentRun(text string) (tea.Model, tea.Cmd) {
	if err := m.commitPendingTimelineEvent(); err != nil {
		m.s.pendingStatus = "✗ " + err.Error()
		return m, nil
	}

	// Clear transient status — it's live-only and never persisted.
	m.s.pendingStatus = ""
	m.s.blocks = append(m.s.blocks, messageBlock{Type: "user", Raw: text})

	// Set session title from the first user message
	if m.session != nil {
		m.session.SetTitle(text, 80)
	}

	// Flush committed timeline events (if any) plus the user message.
	userFlush := m.flushBlocks(len(m.s.blocks))

	m.s.running = true
	m.s.runGen++
	m.s.runStartMsgCount = len(m.agent.Messages())
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

	// tea.Sequence guarantees: committed switch event + user message print
	// before the agent starts. renderTick and spinner can batch concurrently.
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
		// Append blocks but DON'T flush to scrollback yet.
		// Keep them as unflushed so View() renders them directly — this avoids
		// a visual flash where streamCache is cleared but tea.Println hasn't
		// printed yet. Blocks get flushed by the next tool event or agentRunResultMsg.
		if m.s.thinkingText != "" {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "thinking", Raw: m.s.thinkingText,
			})
		}
		if m.s.streamText != "" {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "assistant", Raw: m.s.streamText,
			})
		}
		m.s.streamText = ""
		m.s.thinkingText = ""
		m.s.streamCache = ""
		return nil

	case core.AgentEventToolExecStart:
		m.s.activeTools++
		m.s.streamState = stateToolRunning
		if m.s.activeTools == 1 {
			m.status.SetText("running " + e.ToolName + "...")
		} else {
			m.status.SetText(fmt.Sprintf("running %d tools...", m.s.activeTools))
		}
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "tool", ToolCallID: e.ToolCallID, ToolName: e.ToolName, ToolArgs: e.Args,
		})
		// Don't flush — tool blocks stay in the live View() area until all
		// tools complete, so we can update them in-place with results.
		return nil

	case core.AgentEventToolExecUpdate:
		// Streaming output from a running tool (e.g. bash stdout chunks).
		// Append to the matching block's result so it renders live in View().
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolCallID == e.ToolCallID {
				if e.Result != nil {
					for _, c := range e.Result.Content {
						if c.Type == "text" {
							if b.ToolName == "edit" {
								// Edit emits a diff via onUpdate — store separately
								// so ToolExecEnd doesn't overwrite it.
								b.ToolDiff = c.Text
							} else {
								b.ToolResult += c.Text
							}
						}
					}
				}
				break
			}
		}
		return nil

	case core.AgentEventToolExecEnd:
		m.s.activeTools--
		// Find the matching block and update it with the result.
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolCallID == e.ToolCallID {
				b.ToolDone = true
				b.IsError = e.IsError
				b.ToolResult = toolResultText(e.Result)
				break
			}
		}
		if m.s.activeTools <= 0 {
			m.s.activeTools = 0
			m.s.streamState = stateStreaming
			m.status.SetText("thinking...")
			return m.flushBlocks(len(m.s.blocks))
		}
		if m.s.activeTools == 1 {
			m.status.SetText("running tool...")
		} else {
			m.status.SetText(fmt.Sprintf("running %d tools...", m.s.activeTools))
		}
		return nil

	case core.AgentEventCompactionStart:
		m.status.SetText("compacting context...")
		return nil

	case core.AgentEventCompactionEnd:
		if e.Error != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status",
				Raw:  "⚠ Compaction failed: " + e.Error.Error() + " (continuing with full context)",
			})
		} else if e.Compaction != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status",
				Raw:  fmt.Sprintf("✂ Context compacted (%dK → %dK tokens)", e.Compaction.TokensBefore/1000, e.Compaction.TokensAfter/1000),
			})
		}
		m.status.SetText("thinking...")
		return m.flushBlocks(len(m.s.blocks))
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
	// Does NOT rebuild blocks — preserves event-derived blocks (tool with args, etc.).
	m.patchFromMessages(msg.Messages)

	// Flush any blocks that weren't flushed by events (edge case: dropped events)
	flushCmd := m.flushBlocks(len(m.s.blocks))

	m.s.running = false
	m.s.streamState = stateIdle
	m.s.streamText = ""
	m.s.thinkingText = ""
	m.s.streamCache = ""
	m.status.SetText("")
	m.input.SetEnabled(true)
	m.refreshContextSegment()
	m.accumulateCost(msg.Messages)

	if msg.Err != nil && !errors.Is(msg.Err, context.Canceled) {
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "error", Raw: "Error: " + msg.Err.Error(),
		})
		errorFlush := m.flushBlocks(len(m.s.blocks))
		return m, tea.Batch(flushCmd, errorFlush, m.saveSession(msg.Messages))
	}
	return m, tea.Batch(flushCmd, m.saveSession(msg.Messages))
}

// patchFromMessages corrects the last assistant/thinking block content from
// the source-of-truth messages. Does NOT rebuild — preserves event-derived blocks
// (tool blocks with args and results, etc.) that messages don't contain.
//
// Only searches unflushed blocks (from flushedCount onwards). This prevents
// patching an already-flushed block from a previous turn, which would cause
// flushBlocks to be a no-op (to <= from) and the current turn's content to vanish.
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

	// Search boundary: only patch unflushed blocks (current turn).
	// flushedCount may lag behind flushScheduledCount, but scheduled blocks
	// are already queued for tea.Println — patching them is harmless (content
	// is already rendered). Use flushedCount as the safe lower bound.
	searchFrom := m.s.flushedCount

	// Patch or create thinking block
	if lastThinkingText != "" {
		found := false
		for i := len(m.s.blocks) - 1; i >= searchFrom; i-- {
			if m.s.blocks[i].Type == "thinking" {
				m.s.blocks[i].Raw = lastThinkingText
				found = true
				break
			}
		}
		if !found {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "thinking", Raw: lastThinkingText,
			})
		}
	}

	// Patch or create assistant block
	if lastAssistantText != "" {
		found := false
		for i := len(m.s.blocks) - 1; i >= searchFrom; i-- {
			if m.s.blocks[i].Type == "assistant" {
				m.s.blocks[i].Raw = lastAssistantText
				found = true
				break
			}
		}
		if !found {
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

	// Collect tool_call content from assistant messages to pair with tool_results.
	pendingCalls := make(map[string]core.Content) // ToolCallID → tool_call Content

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
				case c.Type == "tool_call":
					pendingCalls[c.ToolCallID] = c
				}
			}
		case "tool_result":
			tc := pendingCalls[msg.ToolCallID]
			delete(pendingCalls, msg.ToolCallID)

			resultText := ""
			for _, c := range msg.Content {
				if c.Type == "text" {
					resultText += c.Text
				}
			}

			m.s.blocks = append(m.s.blocks, messageBlock{
				Type:       "tool",
				ToolCallID: msg.ToolCallID,
				ToolName:   msg.ToolName,
				ToolArgs:   tc.Arguments,
				ToolResult: strings.TrimSpace(resultText),
				ToolDone:   true,
				IsError:    msg.IsError,
			})
		case "compaction_summary":
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status", Raw: "✂ (conversation compacted)",
			})
		case "session_event":
			if eventType(msg.Custom) == "model_switch" {
				if text := firstTextContent(msg.Content); text != "" {
					m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: text})
				}
			}
		}
	}
}

// --- Commands ---

func (m appModel) handleCommand(cmd string) (tea.Model, tea.Cmd) {
	// Commands with arguments.
	if strings.HasPrefix(cmd, "model ") {
		return m.handleModelSwitch(strings.TrimSpace(cmd[6:]))
	}
	if strings.HasPrefix(cmd, "thinking ") {
		return m.handleThinkingSwitch(strings.TrimSpace(cmd[9:]))
	}
	if strings.HasPrefix(cmd, "permissions ") {
		return m.handlePermissionsSwitch(strings.TrimSpace(cmd[12:]))
	}

	switch cmd {
	case "model", "models":
		// No argument: open the picker.
		currentModel := m.agent.Model()
		m.picker.Open(currentModel.ID, m.scopedModels)
		m.input.SetEnabled(false)
		return m, nil

	case "thinking":
		thinking := m.agent.ThinkingLevel()
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: "thinking: " + thinking})
		return m, m.flushBlocks(len(m.s.blocks))

	case "permissions":
		mode := "yolo"
		if m.permGate != nil {
			mode = string(m.permGate.Mode())
		}
		info := "permissions: " + mode
		if m.permGate != nil {
			if patterns := m.permGate.AllowPatterns(); len(patterns) > 0 {
				info += "\nallow: " + strings.Join(patterns, ", ")
			}
			if rules := m.permGate.Rules(); len(rules) > 0 {
				info += "\nrules: " + strings.Join(rules, ", ")
			}
		}
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: info})
		return m, m.flushBlocks(len(m.s.blocks))

	case "clear":
		if err := m.agent.Reset(); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: err.Error(),
			})
			return m, nil
		}
		m.s.blocks = m.s.blocks[:0]
		m.s.flushedCount = 0
		m.s.flushScheduledCount = 0
		m.s.flushEpoch++
		m.s.streamText = ""
		m.s.thinkingText = ""
		m.s.streamCache = ""
		m.s.pendingStatus = ""
		m.s.pendingTimeline = nil
		m.s.sessionCost = 0
		m.topBar.UpdateCostSegment(0)
		// Delete old session, create fresh one
		if m.sessionStore != nil && m.session != nil {
			_ = m.sessionStore.Delete(m.session.ID)
			m.session = m.sessionStore.Create()
		}
		// Clear screen + scrollback via the system clear command, then
		// tell BT to repaint. ExecProcess bypasses BT's renderer entirely
		// so escape sequences don't interfere with its internal state.
		// Falls back to ClearScreen if clear(1) isn't available.
		return m, tea.Sequence(
			clearScreen(),
			func() tea.Msg { return tea.ClearScreen() },
		)
	case "compact":
		if m.s.running {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: "Cannot compact while agent is running",
			})
			return m, nil
		}
		m.s.running = true
		m.input.SetEnabled(false)
		m.status.SetText("compacting context...")
		agent := m.agent
		ctx := m.baseCtx
		return m, func() tea.Msg {
			payload, err := agent.Compact(ctx)
			return compactResultMsg{Payload: payload, Err: err}
		}

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

// handlePickerKey routes keys to the model picker.
// handlePermissionKey routes keys to the permission prompt.
func (m appModel) handlePermissionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var listenCmd tea.Cmd
	if m.permGate != nil {
		listenCmd = m.waitForPermission()
	}

	// Rule input mode (auto mode, option "Add rule")
	if m.permPrompt.ruleMode {
		switch msg.Type {
		case tea.KeyEnter:
			if rule := m.permPrompt.SaveRule(); rule != "" && m.permGate != nil {
				m.permGate.AddRule(rule)
				m.s.blocks = append(m.s.blocks, messageBlock{
					Type: "status", Raw: fmt.Sprintf("✓ rule added: %s", rule),
				})
			}
			return m, nil // stay on prompt — user still needs to Yes/No
		case tea.KeyEsc:
			m.permPrompt.ruleMode = false
			m.permPrompt.ruleBuf = ""
			return m, nil
		case tea.KeyBackspace:
			if len(m.permPrompt.ruleBuf) > 0 {
				m.permPrompt.ruleBuf = m.permPrompt.ruleBuf[:len(m.permPrompt.ruleBuf)-1]
			} else {
				m.permPrompt.ruleMode = false
			}
			return m, nil
		default:
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
				m.permPrompt.ruleBuf += msg.String()
			}
			return m, nil
		}
	}

	// Amend mode: typing feedback after the selected option
	if m.permPrompt.amending {
		switch msg.Type {
		case tea.KeyEnter:
			m.permPrompt.Confirm()
			return m, listenCmd
		case tea.KeyEsc:
			m.permPrompt.amending = false
			m.permPrompt.amendBuf = ""
			return m, nil
		case tea.KeyBackspace:
			if len(m.permPrompt.amendBuf) > 0 {
				m.permPrompt.amendBuf = m.permPrompt.amendBuf[:len(m.permPrompt.amendBuf)-1]
			} else {
				m.permPrompt.amending = false
			}
			return m, nil
		default:
			if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
				m.permPrompt.amendBuf += msg.String()
			}
			return m, nil
		}
	}

	// Normal mode: navigate and select
	switch msg.Type {
	case tea.KeyUp:
		if m.permPrompt.cursor > 0 {
			m.permPrompt.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.permPrompt.cursor < len(m.permPrompt.options)-1 {
			m.permPrompt.cursor++
		}
		return m, nil
	case tea.KeyEnter:
		opt := m.permPrompt.options[m.permPrompt.cursor]
		if opt.addRule {
			m.permPrompt.ruleMode = true
			m.permPrompt.ruleBuf = ""
			return m, nil
		}
		m.permPrompt.Confirm()
		return m, listenCmd
	case tea.KeyTab:
		opt := m.permPrompt.options[m.permPrompt.cursor]
		if opt.addRule {
			m.permPrompt.ruleMode = true
			m.permPrompt.ruleBuf = ""
			return m, nil
		}
		m.permPrompt.amending = true
		m.permPrompt.amendBuf = ""
		return m, nil
	case tea.KeyEsc, tea.KeyCtrlC:
		m.permPrompt.Cancel()
		m.agent.Abort()
		return m, listenCmd
	case tea.KeyRunes:
		s := msg.String()
		if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
			idx := int(s[0] - '1')
			if idx < len(m.permPrompt.options) {
				opt := m.permPrompt.options[idx]
				if opt.addRule {
					m.permPrompt.cursor = idx
					m.permPrompt.ruleMode = true
					m.permPrompt.ruleBuf = ""
					return m, nil
				}
				m.permPrompt.cursor = idx
				m.permPrompt.Confirm()
				return m, listenCmd
			}
		}
	}
	return m, nil
}

func (m appModel) handleSessionBrowserKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		m.cleanup()
		return m, tea.Quit
	case tea.KeyCtrlN:
		return m.activateSession(m.newSession())
	case tea.KeyUp:
		if m.sessionBrowser.MoveUp() {
			return m, m.loadSessionPreview(m.sessionBrowser.SelectedID())
		}
		return m, nil
	case tea.KeyDown:
		if m.sessionBrowser.MoveDown(m.sessionBrowser.visibleListRows(m.height)) {
			return m, m.loadSessionPreview(m.sessionBrowser.SelectedID())
		}
		return m, nil
	case tea.KeyEnter:
		if sel := m.sessionBrowser.Selected(); sel != nil {
			return m, m.loadSessionByID(sel.ID)
		}
		return m, nil
	case tea.KeyBackspace:
		if m.sessionBrowser.BackspaceFilter() {
			if id := m.sessionBrowser.SelectedID(); id != "" {
				return m, m.loadSessionPreview(id)
			}
		}
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		if msg.String() == "" {
			return m, nil
		}
		if m.sessionBrowser.AppendFilter(msg.String()) {
			if id := m.sessionBrowser.SelectedID(); id != "" {
				return m, m.loadSessionPreview(id)
			}
		}
		return m, nil
	}
	return m, nil
}

func (m appModel) handlePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		// Close without switching.
		m.scopedModels = m.picker.ScopedIDs()
		m.picker.Close()
		m.input.SetEnabled(true)
		return m, nil

	case tea.KeyUp:
		m.picker.MoveUp()
		return m, nil
	case tea.KeyDown:
		m.picker.MoveDown()
		return m, nil

	case tea.KeySpace:
		m.picker.ToggleScoped()
		return m, nil

	case tea.KeyEnter:
		// Select and switch to highlighted model.
		selected := m.picker.Selected()
		m.scopedModels = m.picker.ScopedIDs()
		m.picker.Close()
		m.input.SetEnabled(true)
		return m.switchToModel(selected)

	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "j":
			m.picker.MoveDown()
			return m, nil
		case "k":
			m.picker.MoveUp()
			return m, nil
		}
	}
	return m, nil
}

// switchToModel performs the actual model switch (shared by picker and /model <spec>).
func (m appModel) switchToModel(newModel core.Model) (tea.Model, tea.Cmd) {
	oldModel := m.agent.Model()
	if newModel.Provider == "" {
		newModel.Provider = oldModel.Provider
	}

	var newProvider core.Provider
	if newModel.Provider != oldModel.Provider {
		if m.providerFactory == nil {
			m.s.pendingStatus = fmt.Sprintf("✗ Cannot switch from %s to %s: no provider factory", oldModel.Provider, newModel.Provider)
			m.s.pendingTimeline = nil
			return m, nil
		}
		prov, err := m.providerFactory(newModel)
		if err != nil {
			m.s.pendingStatus = "✗ " + err.Error()
			m.s.pendingTimeline = nil
			return m, nil
		}
		newProvider = prov
	}

	thinkingLevel := m.agent.ThinkingLevel()
	if err := m.agent.Reconfigure(newProvider, newModel, thinkingLevel); err != nil {
		m.s.pendingStatus = "✗ " + err.Error()
		m.s.pendingTimeline = nil
		return m, nil
	}

	name := newModel.Name
	if name == "" {
		name = newModel.ID
	}
	m.modelName = name
	m.topBar.UpdateModelSegment(name)
	m.refreshContextSegment()
	m.s.pendingStatus = ""
	m.s.pendingTimeline = newModelSwitchEvent(newModel)
	return m, nil
}

// cycleScopedModel cycles through scoped/pinned models via Ctrl+P.
func (m appModel) cycleScopedModel() (tea.Model, tea.Cmd) {
	if len(m.scopedModels) == 0 {
		m.status.SetText("no pinned models — use /models to pin some")
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
			return clearThinkingStatusMsg{}
		})
	}

	// Build ordered list of scoped models (deterministic from registry order).
	all := core.ListModels()
	var scoped []core.Model
	for _, e := range all {
		if m.scopedModels[e.Model.ID] {
			scoped = append(scoped, e.Model)
		}
	}

	if len(scoped) == 0 {
		return m, nil
	}

	// Find current model in scoped list, advance to next.
	currentID := m.agent.Model().ID
	nextIdx := 0
	for i, s := range scoped {
		if s.ID == currentID {
			nextIdx = (i + 1) % len(scoped)
			break
		}
	}

	return m.switchToModel(scoped[nextIdx])
}

// handleModelSwitch processes `/model <spec>`.
func (m appModel) handleModelSwitch(spec string) (tea.Model, tea.Cmd) {
	if m.s.running {
		m.s.pendingStatus = "Cannot switch model while agent is running"
		return m, nil
	}

	newModel, known := core.ResolveModel(spec)
	if !known {
		m.s.pendingStatus = fmt.Sprintf("⚠ Unknown model %q — context management disabled", spec)
	}

	return m.switchToModel(newModel)
}

// handlePermissionsSwitch processes `/permissions <mode>`.
func (m appModel) handlePermissionsSwitch(modeStr string) (tea.Model, tea.Cmd) {
	valid := map[string]permission.Mode{
		"yolo": permission.ModeYolo,
		"ask":  permission.ModeAsk,
		"auto": permission.ModeAuto,
	}

	newMode, ok := valid[modeStr]
	if !ok {
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "error", Raw: "Invalid permission mode. Options: yolo, ask, auto",
		})
		return m, m.flushBlocks(len(m.s.blocks))
	}

	cmds := []tea.Cmd{}
	if newMode == permission.ModeYolo {
		m.permGate = nil
		m.agent.SetPermissionCheck(nil)
		m.topBar.UpdatePermissionsSegment("")
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status", Raw: "permissions: yolo (all tools auto-approved)",
		})
	} else {
		if m.permGate == nil {
			m.permGate = permission.New(newMode, permission.Config{})
		} else {
			m.permGate.SetMode(newMode)
		}

		// Auto mode needs an AI evaluator
		if newMode == permission.ModeAuto {
			evalSpec := "haiku"
			evalModel, _ := core.ResolveModel(evalSpec)
			if m.providerFactory != nil {
				prov, err := m.providerFactory(evalModel)
				if err == nil {
					m.permGate.SetEvaluator(permission.NewEvaluator(prov, evalModel))
				} else {
					m.s.blocks = append(m.s.blocks, messageBlock{
						Type: "error", Raw: fmt.Sprintf("auto evaluator unavailable: %v (will fall back to ask)", err),
					})
				}
			}
		}

		m.agent.SetPermissionCheck(m.permGate.Check)
		m.topBar.UpdatePermissionsSegment(string(newMode))
		cmds = append(cmds, m.waitForPermission())
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status", Raw: fmt.Sprintf("permissions: %s", newMode),
		})
	}
	cmds = append(cmds, m.flushBlocks(len(m.s.blocks)))
	return m, tea.Batch(cmds...)
}

// handleThinkingSwitch processes `/thinking <level>`.
func (m appModel) handleThinkingSwitch(level string) (tea.Model, tea.Cmd) {
	if m.s.running {
		m.s.pendingStatus = "✗ Cannot change thinking while agent is running"
		return m, nil
	}

	valid := map[string]bool{"off": true, "minimal": true, "low": true, "medium": true, "high": true}
	if !valid[level] {
		m.s.pendingStatus = "✗ Invalid thinking level. Options: off, minimal, low, medium, high"
		return m, nil
	}

	model := m.agent.Model()
	if err := m.agent.Reconfigure(nil, model, level); err != nil {
		m.s.pendingStatus = "✗ " + err.Error()
		return m, nil
	}

	m.topBar.UpdateThinkingSegment(level)
	m.s.pendingStatus = fmt.Sprintf("✓ Thinking level: %s", level)
	return m, nil
}

func (m appModel) loadSessionBrowser() tea.Cmd {
	store := m.sessionStore
	return func() tea.Msg {
		if store == nil {
			return sessionBrowserLoadedMsg{}
		}
		summaries, err := store.List()
		return sessionBrowserLoadedMsg{Summaries: summaries, Err: err}
	}
}

func (m appModel) loadSessionPreview(id string) tea.Cmd {
	if id == "" {
		return nil
	}
	store := m.sessionStore
	return func() tea.Msg {
		if store == nil {
			return sessionPreviewLoadedMsg{ID: id}
		}
		sess, err := store.Load(id)
		return sessionPreviewLoadedMsg{ID: id, Session: sess, Err: err}
	}
}

func (m appModel) loadSessionByID(id string) tea.Cmd {
	if id == "" {
		return nil
	}
	store := m.sessionStore
	return func() tea.Msg {
		if store == nil {
			return sessionOpenLoadedMsg{}
		}
		sess, err := store.Load(id)
		return sessionOpenLoadedMsg{Session: sess, Err: err}
	}
}

func (m appModel) newSession() *session.Session {
	if m.sessionStore == nil {
		return nil
	}
	return m.sessionStore.Create()
}

func (m appModel) activateSession(sess *session.Session) (tea.Model, tea.Cmd) {
	if sess == nil {
		sess = m.newSession()
		if err := m.agent.Reset(); err != nil {
			m.sessionBrowser.previewErr = err.Error()
			return m, nil
		}
	} else if err := m.agent.LoadState(sess.Messages, sess.CompactionEpoch); err != nil {
		m.sessionBrowser.previewErr = err.Error()
		return m, nil
	}

	m.session = sess
	m.sessionBrowser.Close()
	m.input.SetEnabled(true)
	m.s.blocks = m.s.blocks[:0]
	m.s.flushedCount = 0
	m.s.flushScheduledCount = 0
	m.s.streamText = ""
	m.s.thinkingText = ""
	m.s.streamCache = ""
	m.s.pendingStatus = ""
	m.s.pendingTimeline = nil
	m.s.sessionCost = 0
	m.topBar.UpdateCostSegment(0)

	if sess != nil && len(sess.Messages) > 0 {
		m.rebuildFromMessages(sess.Messages)
	}
	m.refreshContextSegment()

	if len(m.s.blocks) == 0 {
		return m, tea.Sequence(clearScreen(), m.forceRepaint())
	}

	content := renderBlocks(m.s.blocks, m.renderer, m.s.showThinking, false)
	m.s.flushedCount = len(m.s.blocks)
	m.s.flushScheduledCount = len(m.s.blocks)
	return m, tea.Sequence(clearScreen(), tea.Println(content), m.forceRepaint())
}

// --- Session persistence ---

// saveSession returns a Cmd that asynchronously saves the session to disk.
// Takes a snapshot of messages to avoid races with the BT goroutine.
// Returns nil if persistence is disabled.
func (m *appModel) saveSession(msgs []core.AgentMessage) tea.Cmd {
	if m.sessionStore == nil || m.session == nil {
		return nil
	}
	// Snapshot: copy session metadata + messages for the async goroutine.
	// The BT goroutine may modify m.session before the write completes.
	snapshot := *m.session
	snapshot.Messages = make([]core.AgentMessage, len(msgs))
	copy(snapshot.Messages, msgs)
	snapshot.CompactionEpoch = m.agent.CompactionEpoch()

	store := m.sessionStore
	return func() tea.Msg {
		err := store.Save(&snapshot)
		return sessionSavedMsg{err: err}
	}
}

func (m *appModel) commitPendingTimelineEvent() error {
	if m.s.pendingTimeline == nil {
		return nil
	}
	if err := m.agent.AppendMessage(m.s.pendingTimeline.Message); err != nil {
		return err
	}
	m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: m.s.pendingTimeline.Text})
	m.s.pendingTimeline = nil
	return nil
}

func newModelSwitchEvent(model core.Model) *pendingTimelineEvent {
	name := model.Name
	if name == "" {
		name = model.ID
	}
	text := fmt.Sprintf("✓ Switched to %s (%s)", name, model.Provider)
	return &pendingTimelineEvent{
		Text: text,
		Message: core.AgentMessage{
			Message: core.Message{
				Role:      "session_event",
				Content:   []core.Content{core.TextContent(text)},
				Timestamp: time.Now().Unix(),
			},
			Custom: map[string]any{
				"event":    "model_switch",
				"model_id": model.ID,
				"provider": model.Provider,
			},
		},
	}
}

func eventType(custom map[string]any) string {
	if custom == nil {
		return ""
	}
	event, _ := custom["event"].(string)
	return event
}

func firstTextContent(content []core.Content) string {
	for _, c := range content {
		if c.Type == "text" && c.Text != "" {
			return c.Text
		}
	}
	return ""
}

// --- Helpers ---

// waitForEvent returns a Cmd that blocks until the next agent event.
// The run generation comes FROM the tagged event (stamped at production time),
// not captured at Cmd creation time.
// forceRepaint sends a synthetic WindowSizeMsg to force Bubble Tea to
// fully repaint. Fixes ghost lines when the view height shrinks
// (e.g. command palette closing).
func (m appModel) forceRepaint() tea.Cmd {
	w, h := m.width, m.height
	return func() tea.Msg {
		return tea.WindowSizeMsg{Width: w, Height: h}
	}
}

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

// accumulateCost sums Usage from all new assistant messages added during the
// last run (msgs[runStartMsgCount:]) and adds the cost to the session total.
func (m *appModel) accumulateCost(msgs []core.AgentMessage) {
	if msgs == nil || m.agent == nil {
		return
	}
	model := m.agent.Model()
	if model.Pricing == nil {
		return
	}
	start := m.s.runStartMsgCount
	if start > len(msgs) {
		return
	}
	for _, msg := range msgs[start:] {
		if msg.Role == "assistant" && msg.Usage != nil {
			m.s.sessionCost += model.Pricing.Cost(*msg.Usage)
		}
	}
	m.topBar.UpdateCostSegment(m.s.sessionCost)
}

// refreshContextSegment recalculates the context usage percentage and updates
// the top bar segment. Called after agent runs and model switches.
func (m *appModel) refreshContextSegment() {
	if m.agent == nil {
		return
	}
	model := m.agent.Model()
	if model.MaxInput <= 0 {
		m.topBar.Remove(SegmentContext)
		return
	}
	msgs := m.agent.Messages()
	estimate := core.EstimateContextTokens(msgs, "", nil, m.agent.CompactionEpoch())
	pct := (estimate.Tokens * 100) / model.MaxInput
	m.topBar.UpdateContextSegment(pct)
}

// thinkingLevels defines the cycle order for Shift+Tab.
var thinkingLevels = []string{"off", "minimal", "low", "medium", "high"}

// cycleThinkingLevel advances to the next thinking level, wrapping at the end.
func cycleThinkingLevel(current string) string {
	for i, level := range thinkingLevels {
		if level == current {
			return thinkingLevels[(i+1)%len(thinkingLevels)]
		}
	}
	return "medium" // fallback
}
