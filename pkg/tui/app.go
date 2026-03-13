package tui

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/clipboard"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/planmode"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/tasks"
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
	blocks           []messageBlock // conversation history, raw content
	streamText       string         // current streaming assistant text
	thinkingText     string         // current thinking text
	streamCache      string         // cached glamour render of streamText (updated by renderTick)
	dirty            bool           // streamText changed since last render tick
	viewportDirty    bool           // blocks changed, viewport needs refresh on next tick
	running          bool           // agent is running (tick should continue)
	streamState      streamState
	activeTools      int                   // number of tool calls currently executing
	showThinking     bool                  // toggle thinking visibility (Ctrl+T)
	expanded         bool                  // toggle expanded tool results (Ctrl+E)
	initialized      bool                  // first WindowSizeMsg processed
	runGen           uint64                // incremented on each run; events from old runs are ignored
	cleanupOnce      sync.Once             // idempotent cleanup
	pendingStatus    string                // transient generic status shown in View(), never persisted
	pendingTimeline  *pendingTimelineEvent // live timeline event shown in View() until next send
	sessionCost      float64               // accumulated USD cost this session
	runStartMsgCount int                   // message count at start of current run (for delta cost)
	asyncSubagents   int                   // running async subagent count (for status display)
	transcript       bool                  // true when in transcript mode (Ctrl+O)
	fullHistory      bool                  // true when Ctrl+E in transcript mode shows everything
	runStartBlockIdx int                   // block index at start of current run (patch boundary)
	pendingImage     []byte                // raw image bytes waiting to be sent with next message
	pendingImageMime string                // mime type of pending image
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
type pickerPurpose int

const (
	pickerForModelSwitch  pickerPurpose = iota // normal /model switch
	pickerForReviewConfig                       // choosing review model from plan menu
)

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
	viewport       viewport.Model // scrollable conversation area (alt screen mode)
	input          inputModel
	status         statusModel
	picker          pickerModel
	pickerPurpose   pickerPurpose   // why the picker was opened
	thinkingPicker  thinkingPicker
	cmdPalette     cmdPalette
	permPrompt     permissionPrompt
	sessionBrowser sessionBrowser
	topBar         *StatusLine
	bottomBar      *StatusLine

	// Session persistence
	sessionStore session.SessionStore // nil if persistence is disabled
	session      *session.Session // current session (nil if no persistence)
	cwd          string           // working directory for session metadata

	// Display
	modelName string

	// Provider switching
	providerFactory      ProviderFactory
	scopedModels         map[string]bool // model IDs pinned for Ctrl+P cycling
	onPinnedModelsChange func([]string) error // persists pinned model changes (nil = disabled)

	// Permissions
	permGate *permission.Gate

	// Plan mode
	planMode           *planmode.PlanMode
	planMenu           planMenu
	baseSystemPrompt   string // system prompt without plan mode fragments
	reviewGen          uint64 // monotonic counter to detect stale review results
	reviewStreamCh         chan planReviewStreamMsg
	lastReviewResult       *planmode.ReviewResult // last review result (for feedback forwarding)
	lastMenuVariant        planMenuVariant        // to restore after editor
	pendingReviewFeedback  string                 // reviewer feedback to prepend to next user message
	taskStore              *tasks.Store
	taskWidget             taskWidget

	// Subagent status
	subagentCountCh  <-chan int
	subagentNotifyCh <-chan SubagentNotification

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
	SessionStore          session.SessionStore // persistence backend (nil = no persistence)
	Session               *session.Session // session to resume (nil = fresh start)
	StartInSessionBrowser bool             // open the session browser before entering chat
	ModelName             string           // display name for the active model (shown on startup)
	CWD                   string           // working directory for session metadata
	ProviderFactory       ProviderFactory  // creates providers for /model switching (nil = switching disabled)
	PermissionGate        *permission.Gate    // permission gate (nil = yolo, no prompts)
	PinnedModels          []string            // model IDs pre-pinned for Ctrl+P cycling (loaded from global config)
	OnPinnedModelsChange  func([]string) error // called when the user changes pinned models (nil = no persistence)
	SubagentCountCh       <-chan int            // receives running async subagent count updates (nil = disabled)
	SubagentNotifyCh      <-chan SubagentNotification // receives async subagent completion notifications (nil = disabled)
	PlanMode              *planmode.PlanMode   // plan mode instance (nil = disabled)
	TaskStore             *tasks.Store         // task store (nil = no task tracking)
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

	vp := viewport.New(0, 0)
	vp.MouseWheelEnabled = true
	vp.MouseWheelDelta = 3
	vp.KeyMap = viewport.KeyMap{} // disable built-in keys; we route manually

	m := appModel{
		s:               &state{showThinking: true},
		agent:           ag,
		renderer:        newRenderer(80),
		eventCh:         eventCh,
		quit:            quit,
		unsub:           unsub,
		baseCtx:         ctx,
		runGenAddr:      runGenAddr,
		viewport:        vp,
		input:           newInput(),
		status:          newStatus(),
		picker:          newPicker(),
		sessionBrowser:  newSessionBrowser(),
		topBar:          NewStatusLine(statusLineStyle),
		bottomBar:       NewStatusLine(statusLineStyle),
		sessionStore:         cfg.SessionStore,
		session:              cfg.Session,
		cwd:                  cfg.CWD,
		modelName:            cfg.ModelName,
		providerFactory:      cfg.ProviderFactory,
		scopedModels:         pinnedModelsToSet(cfg.PinnedModels),
		onPinnedModelsChange: cfg.OnPinnedModelsChange,
		permGate:             cfg.PermissionGate,
		subagentCountCh:      cfg.SubagentCountCh,
		subagentNotifyCh:     cfg.SubagentNotifyCh,
		planMode:             cfg.PlanMode,
		taskStore:            cfg.TaskStore,
		baseSystemPrompt:     ag.SystemPrompt(),
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
	if m.subagentCountCh != nil {
		cmds = append(cmds, m.waitForSubagentCount())
	}
	if m.subagentNotifyCh != nil {
		cmds = append(cmds, m.waitForSubagentNotify())
	}
	// Sync plan mode on init (handles restored sessions).
	if m.planMode != nil {
		m.syncPermissionCheck()
		m.rebuildSystemPrompt()
		mode := m.planMode.Mode()
		if mode != planmode.ModeOff {
			m.topBar.UpdatePlanSegment(string(mode))
		}
	}
	return tea.Batch(cmds...)
}

func (m appModel) waitForSubagentNotify() tea.Cmd {
	ch := m.subagentNotifyCh
	quit := m.quit
	return func() tea.Msg {
		select {
		case n, ok := <-ch:
			if !ok {
				return nil
			}
			return subagentNotifyMsg{notification: n}
		case <-quit:
			return nil
		}
	}
}

func (m appModel) waitForSubagentCount() tea.Cmd {
	ch := m.subagentCountCh
	quit := m.quit
	return func() tea.Msg {
		select {
		case count, ok := <-ch:
			if !ok {
				return nil
			}
			return asyncSubagentCountMsg{count: count}
		case <-quit:
			return nil
		}
	}
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
			m.updateViewport()
			return m, nil
		}
		// Resize: re-render viewport content (blocks may reflow)
		if sizeChanged && !m.s.transcript {
			m.updateViewport()
		}
		return m, nil

	case tea.MouseMsg:
		if !m.s.transcript && !m.sessionBrowser.active {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case agentEventMsg:
		if msg.RunGen == m.s.runGen {
			m.handleAgentEvent(msg.Event)
		}
		return m, m.waitForEvent()

	case agentDoneMsg:
		// Channel closed or quit signaled. Don't re-subscribe.
		return m, nil

	case agentRunResultMsg:
		return m.handleRunResult(msg)

	case renderTickMsg:
		needsRefresh := false
		if m.s.dirty {
			if m.s.streamText != "" {
				m.s.streamCache = m.renderer.RenderMarkdown(m.s.streamText)
			} else {
				m.s.streamCache = ""
			}
			m.s.dirty = false
			needsRefresh = true
		}
		if m.s.viewportDirty {
			m.s.viewportDirty = false
			needsRefresh = true
		}
		if needsRefresh && !m.s.transcript {
			m.updateViewport()
		}
		// Tick runs while agent is running (not just stateStreaming),
		// so it survives tool_exec transitions. Also during plan review
		// (which doesn't set running but streams into a tool block).
		if m.s.running || m.reviewStreamCh != nil {
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

	case asyncSubagentCountMsg:
		m.s.asyncSubagents = msg.count
		return m, m.waitForSubagentCount()

	case subagentNotifyMsg:
		relistenCmd := m.waitForSubagentNotify()
		if m.s.running {
			// Agent is mid-run — inject as steer. The AgentEventSteer handler
			// will add the subagent block to the chat when it arrives.
			m.agent.Steer(msg.notification.AgentText)
			return m, relistenCmd
		}
		// Agent is idle — start a notification run.
		model, cmd := m.startNotificationRun(msg.notification)
		return model, tea.Batch(cmd, relistenCmd)

	case planReviewStreamMsg:
		if msg.ReviewGen != m.reviewGen {
			return m, nil
		}
		// Append delta to the review tool block.
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolName == "plan_review" && !b.ToolDone {
				b.ToolResult += msg.Delta
				m.s.viewportDirty = true
				break
			}
		}
		return m, m.waitForReviewStream()

	case planReviewResultMsg:
		return m.handlePlanReviewResult(msg)

	case editorDoneMsg:
		if msg.err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: "Editor error: " + msg.err.Error(),
			})
		} else {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status", Raw: "✓ Plan file edited",
			})
		}
		m.planMenu.active = true
		m.planMenu.cursor = 0
		m.planMenu.variant = m.lastMenuVariant
		m.input.SetEnabled(false)
		m.updateViewport()
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
		m.updateViewport()
		var cmds []tea.Cmd
		if m.agent != nil {
			cmds = append(cmds, m.saveSession(m.agent.Messages()))
		}
		return m, tea.Batch(cmds...)

	case permissionRequestMsg:
		// Auto-exit transcript mode so the prompt is visible and actionable
		var cmds []tea.Cmd
		if m.s.transcript {
			m.s.transcript = false
			m.s.fullHistory = false
			m.updateViewport()
			cmds = append(cmds, tea.EnterAltScreen, tea.EnableMouseCellMotion)
		}
		mode := permission.ModeAsk
		if m.permGate != nil {
			mode = m.permGate.Mode()
		}
		m.permPrompt.Show(msg.Request, mode)
		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}
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

	case pinnedModelsSavedMsg:
		// Pinned models saved asynchronously. Errors are silent — not worth interrupting.
		return m, nil

	case clipboardImageMsg:
		if msg.Err != nil {
			switch {
			case errors.Is(msg.Err, clipboard.ErrUnsupported):
				m.status.SetText("clipboard images not supported on this platform")
			case errors.Is(msg.Err, clipboard.ErrNoImage):
				m.status.SetText("no image in clipboard")
			default:
				m.status.SetText("clipboard: " + msg.Err.Error())
			}
			return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
				return clearThinkingStatusMsg{}
			})
		}
		m.s.pendingImage = msg.Data
		m.s.pendingImageMime = msg.MimeType
		kb := len(msg.Data) / 1024
		m.status.SetText(fmt.Sprintf("📎 Image attached (%d KB) — will send with next message", kb))
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

// View renders the full alt-screen layout. The viewport holds durable conversation
// blocks; ephemeral content (spinner, notices) is rendered outside the viewport.
func (m appModel) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	// Transcript mode: minimal footer only
	if m.s.transcript {
		hint := "Ctrl+O: back to chat"
		if m.s.fullHistory {
			hint += " · Ctrl+E: last messages"
		} else {
			hint += " · Ctrl+E: full history"
		}
		return lipgloss.NewStyle().Foreground(ActiveTheme.Overlay1).Render(hint)
	}

	if m.sessionBrowser.active {
		return m.sessionBrowser.View(m.width, m.height)
	}

	// Build bottom chrome (everything below the viewport).
	// Order matches the original inline layout: notices → topBar → input → bottomBar → palette
	var bottomChrome []string

	l := GetActiveLayout()
	if sv := m.status.View(); sv != "" {
		bottomChrome = append(bottomChrome, sv)
	}
	if m.s.pendingStatus != "" {
		bottomChrome = append(bottomChrome, l.RenderLiveNotice(m.s.pendingStatus, m.width, ActiveTheme))
	}
	if m.s.pendingTimeline != nil {
		bottomChrome = append(bottomChrome, l.RenderLiveNotice(m.s.pendingTimeline.Text, m.width, ActiveTheme))
	}
	if m.s.asyncSubagents > 0 {
		label := fmt.Sprintf("⟳ %d subagent running", m.s.asyncSubagents)
		if m.s.asyncSubagents > 1 {
			label = fmt.Sprintf("⟳ %d subagents running", m.s.asyncSubagents)
		}
		bottomChrome = append(bottomChrome, l.RenderLiveNotice(label, m.width, ActiveTheme))
	}
	// Task widget (shown whenever there are tasks).
	if m.taskStore != nil {
		taskList := m.taskStore.Tasks()
		widgetMode := m.taskStore.GetWidgetMode()
		if tv := m.taskWidget.View(taskList, widgetMode, m.width); tv != "" {
			bottomChrome = append(bottomChrome, tv)
		}
	}
	// Input / modal area
	if m.permPrompt.active {
		if pv := m.permPrompt.View(m.width, ActiveTheme); pv != "" {
			bottomChrome = append(bottomChrome, pv)
		}
	} else if m.picker.active {
		if pv := m.picker.View(m.width); pv != "" {
			bottomChrome = append(bottomChrome, pv)
		}
	} else if m.thinkingPicker.active {
		if pv := m.thinkingPicker.View(m.width, ActiveTheme); pv != "" {
			bottomChrome = append(bottomChrome, pv)
		}
	} else if m.planMenu.active {
		if pv := m.planMenu.View(m.width, ActiveTheme); pv != "" {
			bottomChrome = append(bottomChrome, pv)
		}
	} else {
		if iv := m.input.View(); iv != "" {
			bottomChrome = append(bottomChrome, iv)
		}
	}
	if m.topBar != nil {
		if tv := m.topBar.View(m.width); tv != "" {
			bottomChrome = append(bottomChrome, tv)
		}
	}
	if m.bottomBar != nil {
		if bv := m.bottomBar.View(m.width); bv != "" {
			bottomChrome = append(bottomChrome, bv)
		}
	}
	if pv := m.cmdPalette.View(m.width, ActiveTheme); pv != "" {
		bottomChrome = append(bottomChrome, pv)
	}

	// Viewport dimensions are set by updateViewport() (pointer receiver).
	// View() (value receiver) just reads the current size.
	botStr := strings.Join(bottomChrome, "\n")

	// Assemble: viewport + bottom chrome
	var sections []string
	sections = append(sections, m.viewport.View())
	if botStr != "" {
		sections = append(sections, botStr)
	}
	return strings.Join(sections, "\n")
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

	// Thinking picker: intercept all keys.
	if m.thinkingPicker.active {
		return m.handleThinkingPickerKey(msg)
	}

	// Plan action menu: intercept most keys, but let Ctrl+E/Ctrl+O through.
	if m.planMenu.active {
		switch msg.Type {
		case tea.KeyCtrlE, tea.KeyCtrlO:
			// fall through to main switch below
		default:
			return m.handlePlanMenuKey(msg)
		}
	}

	// Transcript mode: only allow mode-switch keys.
	if m.s.transcript {
		switch msg.Type {
		case tea.KeyCtrlO, tea.KeyCtrlE:
			// fall through to main switch below
		case tea.KeyCtrlT:
			m.s.showThinking = !m.s.showThinking
			// Reprint transcript with updated thinking visibility
			content := m.renderTranscriptBlocks(m.s.fullHistory)
			return m, tea.Sequence(clearScreen(), tea.Println(content))
		case tea.KeyCtrlC:
			if m.s.running {
				m.agent.Abort()
				return m, nil
			}
			m.cleanup()
			return m, tea.Quit
		case tea.KeyCtrlD:
			m.cleanup()
			return m, tea.Quit
		default:
			return m, nil
		}
	}

	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		if m.cmdPalette.active {
			m.cmdPalette.Close()
			m.input.textarea.Reset()
			return m, m.forceRepaint()
		}
		// Ctrl+C escalation: clear input → abort agent → quit
		if strings.TrimSpace(m.input.textarea.Value()) != "" {
			m.input.textarea.Reset()
			return m, nil
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
		m.updateViewport()
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

	case tea.KeyCtrlE:
		if m.s.transcript {
			// Toggle full history in transcript mode
			m.s.fullHistory = !m.s.fullHistory
			content := m.renderTranscriptBlocks(m.s.fullHistory)
			return m, tea.Sequence(clearScreen(), tea.Println(content))
		}
		// In alt screen: toggle expanded tool blocks
		if len(m.s.blocks) == 0 {
			return m, nil
		}
		m.s.expanded = !m.s.expanded
		m.updateViewport()
		return m, nil

	case tea.KeyCtrlV:
		if m.s.running {
			return m, nil
		}
		return m, m.checkClipboardImage()

	case tea.KeyCtrlO:
		if m.s.transcript {
			// Return to alt screen
			m.s.transcript = false
			m.s.fullHistory = false
			m.recomputeInputEnabled()
			m.updateViewport()
			return m, tea.Batch(
				tea.EnterAltScreen,
				tea.EnableMouseCellMotion,
			)
		}
		// Enter transcript mode
		if len(m.s.blocks) == 0 {
			return m, nil
		}
		m.s.transcript = true
		m.s.fullHistory = false
		m.input.SetEnabled(false)
		content := m.renderTranscriptBlocks(false)
		return m, tea.Sequence(
			tea.ExitAltScreen,
			tea.DisableMouse,
			clearScreen(),
			tea.Println(content),
		)

	case tea.KeyEnter:
		if msg.Alt {
			// Option/Alt+Enter → pass to textarea for newline insertion
			break
		}
		if m.s.running {
			text := m.input.Submit()
			if text == "" {
				return m, nil
			}
			m.agent.Steer(text)
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
				m.input.textarea.Reset()
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

	case tea.KeyPgUp:
		if !m.s.transcript {
			m.viewport.HalfViewUp()
		}
		return m, nil
	case tea.KeyPgDown:
		if !m.s.transcript {
			m.viewport.HalfViewDown()
		}
		return m, nil

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
		if m.s.streamState == stateIdle && m.input.HistoryUp() {
			m.cmdPalette.Update(m.input.textarea.Value())
			return m, nil
		}

	case tea.KeyDown:
		if m.cmdPalette.active {
			m.cmdPalette.MoveDown()
			return m, nil
		}
		if m.s.streamState == stateIdle && m.input.HistoryDown() {
			m.cmdPalette.Update(m.input.textarea.Value())
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

// --- Agent interaction ---

// prepareRun sets up the common run state (running flag, gen counter, stream state, status).
// Returns the run generation for tagging the result.
func (m *appModel) prepareRun() uint64 {
	m.s.running = true
	m.s.runGen++
	m.s.runStartMsgCount = len(m.agent.Messages())
	m.s.runStartBlockIdx = len(m.s.blocks)
	m.runGenAddr.Store(m.s.runGen)
	m.s.streamState = stateStreaming
	m.s.streamText = ""
	m.s.thinkingText = ""
	m.s.streamCache = ""
	m.input.textarea.Placeholder = "Steer the agent... (Enter to send)"
	m.status.SetText("thinking...")
	return m.s.runGen
}

// launchAgentSend returns a tea.Batch that runs agent.Send and starts
// the render tick and spinner.
func (m appModel) launchAgentSend(text string, gen uint64) tea.Cmd {
	agentRef := m.agent
	baseCtx := m.baseCtx
	return tea.Batch(
		func() tea.Msg {
			msgs, err := agentRef.Send(baseCtx, text)
			return agentRunResultMsg{Err: err, Messages: msgs, RunGen: gen}
		},
		renderTick(),
		m.status.spinner.Tick,
	)
}

// launchAgentSendWithContent returns a tea.Batch that runs agent.SendWithContent
// and starts the render tick and spinner.
func (m appModel) launchAgentSendWithContent(content []core.Content, gen uint64) tea.Cmd {
	agentRef := m.agent
	baseCtx := m.baseCtx
	return tea.Batch(
		func() tea.Msg {
			msgs, err := agentRef.SendWithContent(baseCtx, content)
			return agentRunResultMsg{Err: err, Messages: msgs, RunGen: gen}
		},
		renderTick(),
		m.status.spinner.Tick,
	)
}

// checkClipboardImage reads image data from the system clipboard.
// Calls ReadImage directly — no separate HasImage probe (avoids TOCTOU + extra subprocess).
func (m appModel) checkClipboardImage() tea.Cmd {
	return func() tea.Msg {
		data, mime, err := clipboard.ReadImage()
		return clipboardImageMsg{Data: data, MimeType: mime, Err: err}
	}
}

// startAgentRun sends a prompt to the agent and starts streaming.
func (m appModel) startAgentRun(text string) (tea.Model, tea.Cmd) {
	if err := m.commitPendingTimelineEvent(); err != nil {
		m.s.pendingStatus = "✗ " + err.Error()
		return m, nil
	}

	// Clear transient status — it's live-only and never persisted.
	m.s.pendingStatus = ""

	// Prepend reviewer feedback if user is refining with own instructions.
	if m.pendingReviewFeedback != "" {
		text = "The reviewer found issues with your plan. Address the feedback and my additional instructions, then resubmit with `submit_plan`:\n\n**Reviewer feedback:**\n" + m.pendingReviewFeedback + "\n\n**My instructions:**\n" + text
		m.pendingReviewFeedback = ""
	}

	m.s.blocks = append(m.s.blocks, messageBlock{Type: "user", Raw: text})

	// Consume pending image if any.
	hasImage := m.s.pendingImage != nil
	var imageData []byte
	var imageMime string
	if hasImage {
		imageData = m.s.pendingImage
		imageMime = m.s.pendingImageMime
		m.s.pendingImage = nil
		m.s.pendingImageMime = ""
		kb := len(imageData) / 1024
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status",
			Raw:  fmt.Sprintf("📎 Image attached (%d KB, %s)", kb, imageMime),
		})
	}

	// Set session title from the first user message
	if m.session != nil {
		m.session.SetTitle(text, 80)
	}

	gen := m.prepareRun()
	m.updateViewport()

	if hasImage {
		encoded := base64.StdEncoding.EncodeToString(imageData)
		content := []core.Content{
			core.TextContent(text),
			core.ImageContent(encoded, imageMime),
		}
		return m, m.launchAgentSendWithContent(content, gen)
	}
	return m, m.launchAgentSend(text, gen)
}

// startNotificationRun starts an agent run triggered by a subagent completion
// notification. Shows a subagent block (not a user block) and starts agent.Send
// so the LLM can react to the notification.
func (m appModel) startNotificationRun(n SubagentNotification) (tea.Model, tea.Cmd) {
	if err := m.commitPendingTimelineEvent(); err != nil {
		m.s.pendingStatus = "✗ " + err.Error()
		return m, nil
	}

	m.s.pendingStatus = ""
	m.s.blocks = append(m.s.blocks, messageBlock{
		Type:           "subagent",
		SubagentTask:   n.Task,
		SubagentStatus: n.Status,
		SubagentResult: n.ResultTail,
	})

	gen := m.prepareRun()
	m.updateViewport()
	return m, m.launchAgentSend(n.AgentText, gen)
}

// handleAgentEvent processes a single agent event, updating TUI state.
// Viewport refreshes happen via renderTick, not per-event.
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
		m.s.streamCache = ""
		m.s.streamState = stateStreaming
		m.status.SetText("thinking...")

	case core.AgentEventMessageEnd:
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
		m.s.viewportDirty = true

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
		m.s.viewportDirty = true

	case core.AgentEventToolExecUpdate:
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolCallID == e.ToolCallID {
				if e.Result != nil {
					for _, c := range e.Result.Content {
						if c.Type == "text" {
							if b.ToolName == "edit" {
								b.ToolDiff = c.Text
							} else {
								b.ToolResult += c.Text
							}
						}
					}
					m.s.viewportDirty = true
				}
				break
			}
		}

	case core.AgentEventToolExecEnd:
		m.s.activeTools--
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolCallID == e.ToolCallID {
				b.ToolDone = true
				b.IsError = e.IsError
				b.ToolResult = toolResultText(e.Result)
				break
			}
		}
		m.s.viewportDirty = true
		if m.s.activeTools <= 0 {
			m.s.activeTools = 0
			m.s.streamState = stateStreaming
			m.status.SetText("thinking...")
		} else if m.s.activeTools == 1 {
			m.status.SetText("running tool...")
		} else {
			m.status.SetText(fmt.Sprintf("running %d tools...", m.s.activeTools))
		}
		// Live task progress update when a tasks tool call finishes.
		if e.ToolName == "tasks" && m.taskStore != nil {
			m.refreshTaskDisplay()
		}

	case core.AgentEventSteer:
		if task, status, result, ok := parseSubagentNotification(e.Text); ok {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type:           "subagent",
				SubagentTask:   task,
				SubagentStatus: status,
				SubagentResult: result,
			})
		} else {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "user", Raw: e.Text})
		}
		m.s.viewportDirty = true

	case core.AgentEventCompactionStart:
		m.status.SetText("compacting context...")

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
		m.s.viewportDirty = true
	}
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

	m.s.running = false
	m.s.streamState = stateIdle
	m.s.streamText = ""
	m.s.thinkingText = ""
	m.s.streamCache = ""
	m.status.SetText("")
	m.input.textarea.Placeholder = "Ask anything... (Ctrl+J for newline)"
	m.input.SetEnabled(true)
	m.refreshContextSegment()
	m.accumulateCost(msg.Messages)

	if msg.Err != nil && !errors.Is(msg.Err, context.Canceled) {
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "error", Raw: "Error: " + msg.Err.Error(),
		})
	}

	// Plan mode: check if plan was submitted → show action menu.
	if m.planMode != nil && m.planMode.OnPlanSubmitted() {
		m.topBar.UpdatePlanSegment("ready")
		m.planMenu.OpenPostSubmit()
		m.lastMenuVariant = menuPostSubmit
		m.input.SetEnabled(false)
	}

	// Plan mode: check if all tasks done during execution → auto-exit.
	if m.planMode != nil && m.planMode.Mode() == planmode.ModeExecuting && m.taskStore != nil && m.taskStore.AllDone() {
		m.planMode.Exit()
		m.syncPermissionCheck()
		m.rebuildSystemPrompt()
		m.topBar.UpdatePlanSegment("")
		m.topBar.UpdateTasksSegment(0, 0)
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status", Raw: "✅ All tasks complete — plan mode finished",
		})
	}

	// Update task progress in status bar.
	m.refreshTaskDisplay()

	m.updateViewport()
	return m, m.saveSession(msg.Messages)
}

// patchFromMessages corrects the last assistant/thinking block content from
// the source-of-truth messages. Does NOT rebuild — preserves event-derived blocks
// (tool blocks with args and results, etc.) that messages don't contain.
//
// Only searches blocks from runStartBlockIdx onwards (current run). This prevents
// patching a block from a previous turn, which would leave the current turn's
// content missing from the viewport.
//
// Also creates missing blocks: if agentRunResultMsg arrives before the
// AgentEventMessageEnd event is processed (async emitter race), the assistant/thinking
// blocks won't exist yet. In that case, append them.
func (m *appModel) patchFromMessages(msgs []core.AgentMessage) {
	if msgs == nil {
		return
	}
	// Only look at messages produced during this run (after runStartMsgCount).
	// This prevents re-creating assistant blocks from a previous turn on abort.
	newMsgs := msgs
	if m.s.runStartMsgCount < len(msgs) {
		newMsgs = msgs[m.s.runStartMsgCount:]
	} else {
		newMsgs = nil
	}

	// Extract the final assistant text from new messages only.
	var lastAssistantText string
	var lastThinkingText string
	for i := len(newMsgs) - 1; i >= 0; i-- {
		if newMsgs[i].Role == "assistant" {
			for _, c := range newMsgs[i].Content {
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

	// Search boundary: only patch blocks from the current run.
	searchFrom := m.s.runStartBlockIdx

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
				text := msg.Content[0].Text
				if task, status, result, ok := parseSubagentNotification(text); ok {
					m.s.blocks = append(m.s.blocks, messageBlock{
						Type:           "subagent",
						SubagentTask:   task,
						SubagentStatus: status,
						SubagentResult: result,
					})
				} else {
					m.s.blocks = append(m.s.blocks, messageBlock{
						Type: "user", Raw: text,
					})
				}
				// Show image indicators for user messages with images
				for _, c := range msg.Content {
					if c.Type == "image" {
						kb := len(c.Data) * 3 / 4 / 1024 // base64 → raw size estimate
						m.s.blocks = append(m.s.blocks, messageBlock{
							Type: "status",
							Raw:  fmt.Sprintf("📎 Image attached (%d KB, %s)", kb, c.MimeType),
						})
					}
				}
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
	if strings.HasPrefix(cmd, "tasks ") {
		return m.handleTasksCommand(strings.TrimSpace(cmd[6:]))
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
		m.updateViewport()
		return m, nil

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
		m.updateViewport()
		return m, nil

	case "clear":
		if err := m.agent.Reset(); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: err.Error(),
			})
			return m, nil
		}
		m.s.blocks = m.s.blocks[:0]
		m.s.streamText = ""
		m.s.thinkingText = ""
		m.s.streamCache = ""
		m.s.pendingStatus = ""
		m.s.pendingTimeline = nil
		m.s.pendingImage = nil
		m.s.pendingImageMime = ""
		m.s.sessionCost = 0
		m.s.expanded = false
		m.topBar.UpdateCostSegment(0)
		// Delete old session, create fresh one
		if m.sessionStore != nil && m.session != nil {
			_ = m.sessionStore.Delete(m.session.ID)
			m.session = m.newSession()
		}
		m.updateViewport()
		return m, nil
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

	case "tasks":
		return m.handleTasksCommand("")

	case "plan":
		return m.handlePlanCommand()

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
		prev := m.scopedModels
		m.scopedModels = m.picker.ScopedIDs()
		m.picker.Close()

		if m.pickerPurpose == pickerForReviewConfig {
			// Cancel → reopen plan menu.
			m.planMenu.active = true
			m.planMenu.variant = m.lastMenuVariant
			m.pickerPurpose = pickerForModelSwitch
			return m, nil
		}

		m.input.SetEnabled(true)
		return m, m.savePinnedIfChanged(prev, m.scopedModels)

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
		prev := m.scopedModels
		selected := m.picker.Selected()
		m.scopedModels = m.picker.ScopedIDs()
		m.picker.Close()

		if m.pickerPurpose == pickerForReviewConfig {
			// Store selected model and open thinking picker.
			name := selected.Name
			if name == "" {
				name = selected.ID
			}
			m.planMenu.reviewModelID = selected.ID
			m.planMenu.reviewModel = name
			currentThinking := m.planMenu.reviewThinking
			if currentThinking == "" {
				currentThinking = "medium"
			}
			m.thinkingPicker.Open(currentThinking)
			return m, nil
		}

		m.input.SetEnabled(true)
		m2, switchCmd := m.switchToModel(selected)
		return m2, tea.Batch(switchCmd, m.savePinnedIfChanged(prev, m.scopedModels))

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

// handleThinkingPickerKey routes keys to the thinking level picker.
func (m appModel) handleThinkingPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.thinkingPicker.Close()
		// Cancel → reopen plan menu without changing thinking.
		m.planMenu.active = true
		m.planMenu.variant = m.lastMenuVariant
		m.pickerPurpose = pickerForModelSwitch
		return m, nil

	case tea.KeyUp:
		m.thinkingPicker.MoveUp()
	case tea.KeyDown:
		m.thinkingPicker.MoveDown()

	case tea.KeyEnter:
		selected := m.thinkingPicker.Selected()
		m.thinkingPicker.Close()
		m.planMenu.reviewThinking = selected.value
		// Reopen plan menu with updated config.
		m.planMenu.active = true
		m.planMenu.variant = m.lastMenuVariant
		m.pickerPurpose = pickerForModelSwitch
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status",
			Raw:  "🔍 Review config: " + m.planMenu.reviewModel + " · " + m.planMenu.reviewThinking,
		})
		m.updateViewport()
		return m, nil

	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "j":
			m.thinkingPicker.MoveDown()
		case "k":
			m.thinkingPicker.MoveUp()
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

	if m.session != nil {
		if m.session.Metadata == nil {
			m.session.Metadata = make(map[string]any)
		}
		m.session.Metadata["model"] = fullModelSpec(newModel)
	}
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
		m.updateViewport()
		return m, nil
	}

	cmds := []tea.Cmd{}
	if newMode == permission.ModeYolo {
		m.permGate = nil
		m.syncPermissionCheck()
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

		m.syncPermissionCheck()
		m.topBar.UpdatePermissionsSegment(string(newMode))
		cmds = append(cmds, m.waitForPermission())
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status", Raw: fmt.Sprintf("permissions: %s", newMode),
		})
	}
	m.updateViewport()
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
	sess := m.sessionStore.Create()
	sess.Metadata["cwd"] = m.cwd
	sess.Metadata["model"] = fullModelSpec(m.agent.Model())
	return sess
}

func fullModelSpec(model core.Model) string {
	if model.Provider != "" {
		return model.Provider + "/" + model.ID
	}
	return model.ID
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
	m.s.streamText = ""
	m.s.thinkingText = ""
	m.s.streamCache = ""
	m.s.pendingStatus = ""
	m.s.pendingTimeline = nil
	m.s.pendingImage = nil
	m.s.pendingImageMime = ""
	m.s.sessionCost = 0
	m.topBar.UpdateCostSegment(0)

	// Restore model from session metadata when resuming.
	if sess != nil && sess.Metadata != nil {
		m.restoreModelFromMetadata(sess)
	}

	// Restore plan mode from session metadata.
	if m.planMode != nil {
		if sess != nil && sess.Metadata != nil {
			m.planMode.RestoreState(sess.Metadata)
			m.planMode.ApplyRestoredState()
		} else {
			// New session — ensure plan mode is off.
			m.planMode.Exit()
		}
		m.syncPermissionCheck()
		m.rebuildSystemPrompt()
		mode := m.planMode.Mode()
		if mode != planmode.ModeOff {
			m.topBar.UpdatePlanSegment(string(mode))
		} else {
			m.topBar.UpdatePlanSegment("")
			m.topBar.UpdateTasksSegment(0, 0)
		}
		// Restore task progress display if in executing mode.
		if mode == planmode.ModeExecuting {
			m.refreshTaskDisplay()
		}
	}

	// Restore task store from session metadata.
	if m.taskStore != nil && sess != nil && sess.Metadata != nil {
		m.taskStore.RestoreFromMetadata(sess.Metadata)
		m.refreshTaskDisplay()
	}

	if sess != nil && len(sess.Messages) > 0 {
		m.rebuildFromMessages(sess.Messages)
	}
	m.refreshContextSegment()
	m.updateViewport()
	return m, m.forceRepaint()
}

func (m *appModel) restoreModelFromMetadata(sess *session.Session) {
	spec, ok := sess.Metadata["model"].(string)
	if !ok || spec == "" {
		return
	}
	model, _ := core.ResolveModel(spec)
	if model.ID == "" {
		return
	}
	current := m.agent.Model()
	if model.ID == current.ID {
		return
	}
	var prov core.Provider
	if model.Provider != current.Provider {
		if m.providerFactory == nil {
			return
		}
		p, err := m.providerFactory(model)
		if err != nil {
			return
		}
		prov = p
	}
	if err := m.agent.Reconfigure(prov, model, m.agent.ThinkingLevel()); err != nil {
		return
	}
	name := model.Name
	if name == "" {
		name = model.ID
	}
	m.modelName = name
	m.topBar.UpdateModelSegment(m.modelName)
}

// --- Pinned models ---

// savePinnedIfChanged only persists if the set actually changed.
func (m appModel) savePinnedIfChanged(prev, curr map[string]bool) tea.Cmd {
	if pinnedSetsEqual(prev, curr) {
		return nil
	}
	return m.savePinnedModels(curr)
}

// savePinnedModels runs the OnPinnedModelsChange callback in the background.
// Only fires if a callback is configured. Returns nil otherwise.
func (m appModel) savePinnedModels(ids map[string]bool) tea.Cmd {
	fn := m.onPinnedModelsChange
	if fn == nil {
		return nil
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	slices.Sort(list)
	return func() tea.Msg {
		return pinnedModelsSavedMsg{err: fn(list)}
	}
}

// pinnedModelsToSet converts a slice of model IDs to the map used internally.
func pinnedModelsToSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

func pinnedSetsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if !b[id] {
			return false
		}
	}
	return true
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
	// Deep-copy metadata map to avoid races with model switches.
	if snapshot.Metadata != nil {
		meta := make(map[string]any, len(snapshot.Metadata))
		for k, v := range snapshot.Metadata {
			meta[k] = v
		}
		snapshot.Metadata = meta
	}
	// Persist plan mode state.
	if m.planMode != nil {
		pmState := m.planMode.SaveState()
		if snapshot.Metadata == nil {
			snapshot.Metadata = make(map[string]any)
		}
		for k, v := range pmState {
			snapshot.Metadata[k] = v
		}
	}
	// Persist task store state.
	if m.taskStore != nil {
		tsState := m.taskStore.SaveToMetadata()
		if snapshot.Metadata == nil {
			snapshot.Metadata = make(map[string]any)
		}
		for k, v := range tsState {
			snapshot.Metadata[k] = v
		}
	}

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

// --- Viewport ---

// updateViewport re-renders conversation blocks into the viewport.
// Only durable content (blocks + streaming text). Ephemeral content
// (spinner, notices) is rendered outside the viewport in View().
// Also recalculates viewport dimensions from current terminal size.
// refreshTaskDisplay updates the status bar task segment and plan segment with current progress.
func (m *appModel) refreshTaskDisplay() {
	if m.taskStore == nil {
		return
	}
	done, total := m.taskStore.Progress()
	m.topBar.UpdateTasksSegment(done, total)
	if m.planMode != nil && m.planMode.Mode() == planmode.ModeExecuting {
		if total > 0 {
			m.topBar.UpdatePlanSegment(fmt.Sprintf("executing 📋 %d/%d", done, total))
		} else {
			m.topBar.UpdatePlanSegment("executing")
		}
	}
	m.s.viewportDirty = true
}

func (m *appModel) updateViewport() {
	// Check scroll position BEFORE resizing — resizing can change maxYOffset
	// and make AtBottom() return false even though the user was at the bottom.
	wasAtBottom := m.viewport.AtBottom() || m.viewport.TotalLineCount() == 0
	m.resizeViewport()
	content := m.renderViewportContent()
	m.viewport.SetContent(content)
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
}

// resizeViewport recalculates viewport dimensions from terminal size and chrome heights.
func (m *appModel) resizeViewport() {
	if m.width == 0 || m.height == 0 {
		return
	}
	chromeH := m.computeChromeHeight()
	vpH := m.height - chromeH
	if vpH < 1 {
		vpH = 1
	}
	m.viewport.Width = m.width
	m.viewport.Height = vpH
	if m.viewport.PastBottom() {
		m.viewport.GotoBottom()
	}
}

// computeChromeHeight returns the total lines used by non-viewport chrome.
// Must match View()'s bottom chrome components exactly.
func (m *appModel) computeChromeHeight() int {
	h := 0

	l := GetActiveLayout()
	if sv := m.status.View(); sv != "" {
		h += lipgloss.Height(sv)
	}
	if m.s.pendingStatus != "" {
		h += lipgloss.Height(l.RenderLiveNotice(m.s.pendingStatus, m.width, ActiveTheme))
	}
	if m.s.pendingTimeline != nil {
		h += lipgloss.Height(l.RenderLiveNotice(m.s.pendingTimeline.Text, m.width, ActiveTheme))
	}
	if m.s.asyncSubagents > 0 {
		h++
	}
	// Task widget height.
	if m.taskStore != nil {
		taskList := m.taskStore.Tasks()
		widgetMode := m.taskStore.GetWidgetMode()
		if tv := m.taskWidget.View(taskList, widgetMode, m.width); tv != "" {
			h += lipgloss.Height(tv)
		}
	}
	if m.permPrompt.active {
		if pv := m.permPrompt.View(m.width, ActiveTheme); pv != "" {
			h += lipgloss.Height(pv)
		}
	} else if m.picker.active {
		if pv := m.picker.View(m.width); pv != "" {
			h += lipgloss.Height(pv)
		}
	} else if m.thinkingPicker.active {
		if pv := m.thinkingPicker.View(m.width, ActiveTheme); pv != "" {
			h += lipgloss.Height(pv)
		}
	} else if m.planMenu.active {
		if pv := m.planMenu.View(m.width, ActiveTheme); pv != "" {
			h += lipgloss.Height(pv)
		}
	} else {
		if iv := m.input.View(); iv != "" {
			h += lipgloss.Height(iv)
		}
	}
	if m.topBar != nil {
		if tv := m.topBar.View(m.width); tv != "" {
			h += lipgloss.Height(tv)
		}
	}
	if m.bottomBar != nil {
		if bv := m.bottomBar.View(m.width); bv != "" {
			h += lipgloss.Height(bv)
		}
	}
	if pv := m.cmdPalette.View(m.width, ActiveTheme); pv != "" {
		h += lipgloss.Height(pv)
	}

	h++ // gap between viewport and bottom chrome
	return h
}

// renderViewportContent renders blocks for the viewport (last N turns + streaming).
func (m *appModel) renderViewportContent() string {
	blocks := m.visibleBlocks()

	var parts []string
	for _, block := range blocks {
		if s := renderSingleBlockEx(block, m.renderer, m.s.showThinking, m.s.expanded); s != "" {
			parts = append(parts, s)
		}
	}

	// Append streaming content
	if m.s.thinkingText != "" && m.s.showThinking {
		parts = append(parts, GetActiveLayout().RenderThinking(m.s.thinkingText, m.width, ActiveTheme))
	}
	if m.s.streamCache != "" {
		parts = append(parts, GetActiveLayout().RenderAssistantText(m.s.streamCache, m.width))
	}

	return strings.Join(parts, "\n\n")
}

// visibleBlocks returns all blocks for the viewport. The viewport scrolls,
// so no turn-based limiting is needed.
func (m *appModel) visibleBlocks() []messageBlock {
	return m.s.blocks
}

const transcriptTurnLimit = 10

// renderTranscriptBlocks renders blocks for transcript mode.
// fullHistory=false shows last N turns, fullHistory=true shows everything.
// Always rendered expanded.
func (m *appModel) renderTranscriptBlocks(fullHistory bool) string {
	blocks := m.s.blocks
	if !fullHistory && len(blocks) > 0 {
		turns := 0
		start := 0
		for i := len(blocks) - 1; i >= 0; i-- {
			if blocks[i].Type == "user" || blocks[i].Type == "subagent" {
				turns++
				if turns > transcriptTurnLimit {
					break
				}
				start = i
			}
		}
		blocks = blocks[start:]
	}
	return renderBlocks(blocks, m.renderer, m.s.showThinking, true)
}

// recomputeInputEnabled sets input enabled/disabled based on current state.
// Used when exiting transcript mode to avoid unconditionally enabling input.
func (m *appModel) recomputeInputEnabled() {
	enabled := !m.s.running && !m.permPrompt.active && !m.picker.active && !m.sessionBrowser.active && !m.planMenu.active && !m.thinkingPicker.active
	m.input.SetEnabled(enabled)
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

// parseSubagentNotification detects steer messages formatted as subagent
// completion notifications and extracts the components. Returns false for
// user-typed steer messages.
func parseSubagentNotification(text string) (task, status, result string, ok bool) {
	prefixes := map[string]string{
		"[subagent completed] ": "completed",
		"[subagent failed] ":    "failed",
		"[subagent cancelled] ": "cancelled",
	}
	for prefix, s := range prefixes {
		if strings.HasPrefix(text, prefix) {
			status = s
			rest := text[len(prefix):]
			// Extract task from "Job <id> finished.\nTask: <task>\n..."
			lines := strings.SplitN(rest, "\n", 3)
			if len(lines) >= 2 {
				taskLine := lines[1]
				if strings.HasPrefix(taskLine, "Task: ") {
					task = strings.TrimPrefix(taskLine, "Task: ")
				}
			}
			// Everything after the task line is the result
			if len(lines) >= 3 {
				result = strings.TrimSpace(lines[2])
				// Strip known prefixes
				for _, p := range []string{"Result (last 50 lines):\n", "Error: "} {
					if strings.HasPrefix(result, p) {
						result = strings.TrimSpace(result[len(p):])
						break
					}
				}
			}
			return task, status, result, true
		}
	}
	return "", "", "", false
}

// --- Plan mode ---

func (m appModel) handleTasksCommand(args string) (tea.Model, tea.Cmd) {
	if m.taskStore == nil {
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Task tracking not available"})
		return m, nil
	}

	parts := strings.Fields(args)

	// /tasks — show task list.
	if len(parts) == 0 {
		taskList := m.taskStore.Tasks()
		if len(taskList) == 0 {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: "No tasks"})
		} else {
			done := 0
			var lines []string
			for _, t := range taskList {
				icon := "☐"
				if t.Status == "done" {
					icon = "☑"
					done++
				}
				lines = append(lines, fmt.Sprintf("%s #%d: %s", icon, t.ID, t.Title))
			}
			lines = append(lines, fmt.Sprintf("\n%d/%d complete", done, len(taskList)))
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: strings.Join(lines, "\n")})
		}
		m.updateViewport()
		return m, nil
	}

	switch parts[0] {
	case "done":
		if len(parts) < 2 {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Usage: /tasks done <id>"})
			m.updateViewport()
			return m, nil
		}
		var id int
		if _, err := fmt.Sscanf(parts[1], "%d", &id); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Invalid task ID: " + parts[1]})
			m.updateViewport()
			return m, nil
		}
		if !m.taskStore.MarkDone(id) {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: fmt.Sprintf("Task #%d not found", id)})
			m.updateViewport()
			return m, nil
		}
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: fmt.Sprintf("✅ Task #%d marked done", id)})
		m.refreshTaskDisplay()
		m.updateViewport()
		return m, nil

	case "reset":
		m.taskStore.Reset()
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: "Tasks cleared"})
		m.refreshTaskDisplay()
		m.updateViewport()
		return m, nil

	case "show":
		if len(parts) >= 2 {
			switch tasks.WidgetMode(parts[1]) {
			case tasks.WidgetAll, tasks.WidgetCurrent, tasks.WidgetHidden:
				m.taskStore.SetWidgetMode(tasks.WidgetMode(parts[1]))
			default:
				m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Usage: /tasks show [all|current|hidden]"})
				m.updateViewport()
				return m, nil
			}
		} else {
			// Cycle: all → current → hidden → all.
			current := m.taskStore.GetWidgetMode()
			switch current {
			case tasks.WidgetAll:
				m.taskStore.SetWidgetMode(tasks.WidgetCurrent)
			case tasks.WidgetCurrent:
				m.taskStore.SetWidgetMode(tasks.WidgetHidden)
			default:
				m.taskStore.SetWidgetMode(tasks.WidgetAll)
			}
		}
		mode := m.taskStore.GetWidgetMode()
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: fmt.Sprintf("Task widget: %s", mode)})
		m.updateViewport()
		return m, nil

	default:
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Usage: /tasks [done <n> | reset | show [all|current|hidden]]"})
		m.updateViewport()
		return m, nil
	}
}

func (m appModel) handlePlanCommand() (tea.Model, tea.Cmd) {
	if m.planMode == nil {
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Plan mode not available"})
		return m, nil
	}
	if m.s.running {
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Cannot toggle plan mode while agent is running"})
		return m, nil
	}

	mode := m.planMode.Mode()
	switch mode {
	case planmode.ModeReady:
		// Already have a plan — show the action menu.
		m.planMenu.OpenPostSubmit()
		m.lastMenuVariant = menuPostSubmit
		m.input.SetEnabled(false)
		m.updateViewport()
		return m, nil

	case planmode.ModePlanning:
		// In the middle of planning — exit.
		m.planMode.Exit()
		m.syncPermissionCheck()
		m.rebuildSystemPrompt()
		m.topBar.UpdatePlanSegment("")
		m.topBar.UpdateTasksSegment(0, 0)
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: "Plan mode disabled"})
		m.updateViewport()
		return m, nil

	case planmode.ModeExecuting:
		// Can't toggle during execution.
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Plan is currently executing — wait for it to finish or use /plan after completion"})
		m.updateViewport()
		return m, nil

	case planmode.ModeReviewing:
		// Can't toggle during review.
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Plan review in progress"})
		m.updateViewport()
		return m, nil
	}

	planPath, err := m.planMode.Enter()
	if err != nil {
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Plan mode: " + err.Error()})
		return m, nil
	}
	m.syncPermissionCheck()
	m.rebuildSystemPrompt()
	m.topBar.UpdatePlanSegment("planning")
	m.s.blocks = append(m.s.blocks, messageBlock{
		Type: "status",
		Raw:  "📋 Plan mode enabled — plan file: " + planPath,
	})
	m.updateViewport()
	return m, nil
}

// syncPermissionCheck rebuilds the composed permission check from all sources.
// Always call this when plan mode or permission gate state changes.
func (m *appModel) syncPermissionCheck() {
	pm := m.planMode
	gate := m.permGate
	_ = m.agent.SetPermissionCheck(func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
		// Plan mode filter first (if plan mode is active).
		if pm != nil {
			if allowed, reason := pm.FilterToolCall(name, args); !allowed {
				return &core.ToolCallDecision{Block: true, Reason: reason}
			}
		}
		// Permission gate second (if configured).
		if gate != nil {
			return gate.Check(ctx, name, args)
		}
		return nil
	})
}

// rebuildSystemPrompt updates the agent's system prompt with plan mode fragments.
func (m *appModel) rebuildSystemPrompt() {
	if m.planMode == nil {
		return
	}
	prompt := m.baseSystemPrompt
	if p := planmode.PlanningPrompt(m.planMode.PlanFilePath()); m.planMode.Mode() == planmode.ModePlanning && p != "" {
		prompt += "\n\n" + p
	}
	if p := planmode.ExecutionPrompt(m.planMode.PlanFilePath()); m.planMode.Mode() == planmode.ModeExecuting && p != "" {
		prompt += "\n\n" + p
	}
	_ = m.agent.SetSystemPrompt(prompt)
}

// handlePlanMenuKey routes keys to the plan action menu.
func (m appModel) handlePlanMenuKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		m.planMenu.MoveUp()
	case tea.KeyDown:
		m.planMenu.MoveDown()
	case tea.KeyEnter:
		action := m.planMenu.Selected()
		m.planMenu.Close()
		m.input.SetEnabled(true)
		return m.executePlanAction(action)
	case tea.KeyEsc, tea.KeyCtrlC:
		m.planMenu.Close()
		m.input.SetEnabled(true)
	case tea.KeyTab:
		// Tab on a review action → open model picker for review config.
		actions := m.planMenu.actions()
		if m.planMenu.cursor < len(actions) && actions[m.planMenu.cursor].action == planActionReview {
			m.planMenu.Close()
			currentModel := m.agent.Model()
			m.picker.Open(currentModel.ID, m.scopedModels)
			m.pickerPurpose = pickerForReviewConfig
		}
	}
	return m, nil
}

func (m appModel) executePlanAction(action planAction) (tea.Model, tea.Cmd) {
	switch action {
	case planActionExecuteClean:
		if err := m.agent.Reset(); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: err.Error()})
			return m, nil
		}
		m.s.blocks = m.s.blocks[:0]
		m.s.sessionCost = 0
		m.topBar.UpdateCostSegment(0)
		m.planMode.StartExecution()
		m.syncPermissionCheck()
		m.rebuildSystemPrompt()
		m.topBar.UpdatePlanSegment("executing")
		planFile := m.planMode.PlanFilePath()
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status", Raw: "▶ Executing plan (clean context)",
		})
		m.updateViewport()
		return m.sendMessage("Execute the plan saved at " + planFile + ". Read the plan file first, then create tasks and begin implementing.")

	case planActionExecuteKeep:
		m.planMode.StartExecution()
		m.syncPermissionCheck()
		m.rebuildSystemPrompt()
		m.topBar.UpdatePlanSegment("executing")
		planFile := m.planMode.PlanFilePath()
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status", Raw: "▶ Executing plan (keeping context)",
		})
		m.updateViewport()
		return m.sendMessage("Execute the plan saved at " + planFile + ". Read the plan file first, then create tasks and begin implementing.")

	case planActionReview:
		m.planMode.StartReview()
		m.topBar.UpdatePlanSegment("reviewing")
		m.input.SetEnabled(false)
		m.status.SetText("reviewing plan...")
		// Build review args for the tool block display.
		reviewArgs := map[string]any{"plan": m.planMode.PlanFilePath()}
		modelLabel := "default"
		if m.planMenu.reviewModel != "" {
			modelLabel = m.planMenu.reviewModel
			reviewArgs["model"] = modelLabel
		}
		thinkingLabel := "default"
		if m.planMenu.reviewThinking != "" {
			thinkingLabel = m.planMenu.reviewThinking
			reviewArgs["thinking"] = thinkingLabel
		}
		// Create a tool-like block for the review (streaming output will fill ToolResult).
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type:       "tool",
			ToolCallID: fmt.Sprintf("review_%d", m.reviewGen+1),
			ToolName:   "plan_review",
			ToolArgs:   reviewArgs,
		})
		m.updateViewport()
		reviewCmd := m.runPlanReview()
		return m, tea.Batch(reviewCmd, m.waitForReviewStream(), m.status.spinner.Tick, renderTick())

	case planActionRefine:
		m.planMode.ContinueRefining()
		m.rebuildSystemPrompt()
		m.topBar.UpdatePlanSegment("planning")
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status", Raw: "✏️ Continuing to refine plan",
		})
		m.updateViewport()
		return m, nil

	case planActionEditor:
		m.lastMenuVariant = m.planMenu.variant
		return m, openInEditor(m.planMode.PlanFilePath())

	case planActionAutoRefine:
		// Send reviewer feedback to model for auto-refinement.
		m.planMode.ContinueRefining()
		m.rebuildSystemPrompt()
		m.topBar.UpdatePlanSegment("planning")
		feedback := ""
		if m.lastReviewResult != nil {
			feedback = m.lastReviewResult.Feedback
		}
		msg := "The reviewer found issues with your plan. Address the feedback and resubmit with `submit_plan`:\n\n" + feedback
		return m.sendMessage(msg)

	case planActionRefineWithOwn:
		// Let user add instructions, then combine with reviewer feedback.
		m.planMode.ContinueRefining()
		m.rebuildSystemPrompt()
		m.topBar.UpdatePlanSegment("planning")
		m.input.SetEnabled(true)
		feedback := ""
		if m.lastReviewResult != nil {
			feedback = m.lastReviewResult.Feedback
		}
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status",
			Raw:  "✏️ Type your instructions. They'll be sent along with the reviewer feedback.",
		})
		// Store feedback so the next send can prepend it.
		m.pendingReviewFeedback = feedback
		m.updateViewport()
		return m, nil

	case planActionExecAnywayClean:
		// Same as execute clean but from a rejected review.
		return m.executePlanAction(planActionExecuteClean)

	case planActionExecAnywayKeep:
		// Same as execute keep but from a rejected review.
		return m.executePlanAction(planActionExecuteKeep)

	case planActionStayInPlanMode:
		m.planMode.ContinueRefining()
		m.rebuildSystemPrompt()
		m.topBar.UpdatePlanSegment("planning")
		m.input.SetEnabled(true)
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status", Raw: "⏸ Staying in plan mode — edit the plan manually or keep refining",
		})
		m.updateViewport()
		return m, nil
	}
	return m, nil
}

// sendMessage sends a programmatic user message and starts the agent run.
// Uses the same pipeline as regular input (prepareRun + launchAgentSend)
// to get proper render ticks and spinner.
func (m appModel) sendMessage(text string) (tea.Model, tea.Cmd) {
	m.s.blocks = append(m.s.blocks, messageBlock{Type: "user", Raw: text})
	gen := m.prepareRun()
	m.input.SetEnabled(false)
	m.updateViewport()
	return m, m.launchAgentSend(text, gen)
}

// planReviewStreamMsg carries a text delta from the reviewer.
type planReviewStreamMsg struct {
	Delta     string
	ReviewGen uint64
}

// planReviewResultMsg carries the final result of an async plan review.
type planReviewResultMsg struct {
	Result    planmode.ReviewResult
	Err       error
	ReviewGen uint64
}

func (m *appModel) runPlanReview() tea.Cmd {
	m.reviewGen++
	gen := m.reviewGen
	// Create a buffered channel for stream deltas.
	ch := make(chan planReviewStreamMsg, 64)
	m.reviewStreamCh = ch
	pm := m.planMode
	planPath := pm.PlanFilePath()
	reviewCfg := pm.GetReviewConfig()
	// Apply per-run overrides from the plan menu.
	if m.planMenu.reviewModelID != "" {
		resolved, _ := core.ResolveModel(m.planMenu.reviewModelID)
		if resolved.ID != "" {
			reviewCfg.Model = resolved
		}
	}
	if m.planMenu.reviewThinking != "" {
		reviewCfg.ThinkingLevel = m.planMenu.reviewThinking
	}
	ctx := m.baseCtx
	return func() tea.Msg {
		onStream := func(delta string) {
			// Non-blocking send — drop if buffer full (UI will catch up on next delta).
			select {
			case ch <- planReviewStreamMsg{Delta: delta, ReviewGen: gen}:
			default:
			}
		}
		result, err := planmode.Review(ctx, reviewCfg, planPath, onStream)
		close(ch)
		return planReviewResultMsg{Result: result, Err: err, ReviewGen: gen}
	}
}

// waitForReviewStream returns a Cmd that reads one delta from the review stream channel.
func (m appModel) waitForReviewStream() tea.Cmd {
	ch := m.reviewStreamCh
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil // channel closed, result msg will arrive separately
		}
		return msg
	}
}

func (m appModel) handlePlanReviewResult(msg planReviewResultMsg) (tea.Model, tea.Cmd) {
	// Ignore stale results (mode changed while review was running).
	if msg.ReviewGen != m.reviewGen {
		return m, nil
	}
	if m.planMode.Mode() != planmode.ModeReviewing {
		return m, nil
	}
	m.planMode.ReviewDone()
	m.topBar.UpdatePlanSegment("ready")
	m.status.SetText("")
	m.reviewStreamCh = nil

	if msg.Err != nil {
		// Mark review block as error.
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolName == "plan_review" && !b.ToolDone {
				b.ToolDone = true
				b.IsError = true
				b.ToolResult = "Review failed: " + msg.Err.Error()
				break
			}
		}
		m.planMenu.OpenPostSubmit()
		m.lastMenuVariant = menuPostSubmit
	} else {
		// Mark review block as done with the final result.
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolName == "plan_review" && !b.ToolDone {
				b.ToolDone = true
				b.ToolResult = msg.Result.Feedback
				break
			}
		}
		m.lastReviewResult = &msg.Result
		if msg.Result.Approved {
			m.planMenu.OpenPostReviewApproved()
			m.lastMenuVariant = menuPostReviewApproved
		} else {
			m.planMenu.OpenPostReviewRejected()
			m.lastMenuVariant = menuPostReviewRejected
		}
	}

	m.input.SetEnabled(false)
	m.updateViewport()
	return m, nil
}

// openInEditor opens a file in $EDITOR using tea.ExecProcess.
func openInEditor(path string) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	//nolint:gosec // editor is user-controlled by design
	c := exec.Command(editor, path)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editorDoneMsg{err: err}
	})
}

type editorDoneMsg struct {
	err error
}

