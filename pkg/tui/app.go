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
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/clipboard"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/planmode"
	promptpkg "github.com/ealeixandre/moa/pkg/prompt"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/tasks"
	"github.com/ealeixandre/moa/pkg/verify"
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
	pickerForReviewConfig                      // choosing review model from plan menu
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
	picker         pickerModel
	pickerPurpose  pickerPurpose // why the picker was opened
	thinkingPicker thinkingPicker
	cmdPalette     cmdPalette
	permPrompt     permissionPrompt
	sessionBrowser sessionBrowser
	topBar         *StatusLine
	bottomBar      *StatusLine

	// Session persistence
	sessionStore session.SessionStore // nil if persistence is disabled
	session      *session.Session     // current session (nil if no persistence)
	cwd          string               // working directory for session metadata

	// Display
	modelName string

	// Provider switching
	providerFactory      ProviderFactory
	scopedModels         map[string]bool      // model IDs pinned for Ctrl+P cycling
	onPinnedModelsChange func([]string) error // persists pinned model changes (nil = disabled)

	// Permissions
	permGate *permission.Gate

	// Verify
	verifyCancel context.CancelFunc // non-nil while /verify is running

	// Plan mode
	planMode              *planmode.PlanMode
	planMenu              planMenu
	baseSystemPrompt      string // system prompt without plan mode fragments
	reviewGen             uint64 // monotonic counter to detect stale review results
	reviewStreamCh        chan planReviewStreamMsg
	lastReviewResult      *planmode.ReviewResult // last review result (for feedback forwarding)
	lastMenuVariant       planMenuVariant        // to restore after editor
	pendingReviewFeedback string                 // reviewer feedback to prepend to next user message
	taskStore             *tasks.Store
	taskWidget            taskWidget

	// Prompt templates
	promptTemplates []promptpkg.Template

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
	SessionStore          session.SessionStore        // persistence backend (nil = no persistence)
	Session               *session.Session            // session to resume (nil = fresh start)
	StartInSessionBrowser bool                        // open the session browser before entering chat
	ModelName             string                      // display name for the active model (shown on startup)
	CWD                   string                      // working directory for session metadata
	ProviderFactory       ProviderFactory             // creates providers for /model switching (nil = switching disabled)
	PermissionGate        *permission.Gate            // permission gate (nil = yolo, no prompts)
	PinnedModels          []string                    // model IDs pre-pinned for Ctrl+P cycling (loaded from global config)
	OnPinnedModelsChange  func([]string) error        // called when the user changes pinned models (nil = no persistence)
	SubagentCountCh       <-chan int                  // receives running async subagent count updates (nil = disabled)
	SubagentNotifyCh      <-chan SubagentNotification // receives async subagent completion notifications (nil = disabled)
	PlanMode              *planmode.PlanMode          // plan mode instance (nil = disabled)
	TaskStore             *tasks.Store                // task store (nil = no task tracking)
	PromptTemplates       []promptpkg.Template        // available prompt templates (nil = none)
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
		s:                    &state{showThinking: true},
		agent:                ag,
		renderer:             newRenderer(80),
		eventCh:              eventCh,
		quit:                 quit,
		unsub:                unsub,
		baseCtx:              ctx,
		runGenAddr:           runGenAddr,
		viewport:             vp,
		input:                newInput(),
		status:               newStatus(),
		picker:               newPicker(),
		sessionBrowser:       newSessionBrowser(),
		topBar:               NewStatusLine(statusLineStyle),
		bottomBar:            NewStatusLine(statusLineStyle),
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
		promptTemplates:      cfg.PromptTemplates,
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

	case verifyResultMsg:
		m.s.running = false
		m.input.SetEnabled(true)
		m.verifyCancel = nil
		m.status.SetText("")
		if msg.Err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: msg.Err.Error()})
		} else {
			text := verify.FormatResult(*msg.Result)
			blockType := "status"
			if !msg.Result.AllPass {
				blockType = "error"
			}
			m.s.blocks = append(m.s.blocks, messageBlock{Type: blockType, Raw: text})
		}
		m.updateViewport()
		return m, nil

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
		// TODO: consider a subtle status indicator for save failures
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
			if m.verifyCancel != nil {
				m.verifyCancel()
				return m, nil
			}
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
		// Ctrl+C escalation: clear input → cancel verify → abort agent → quit
		if strings.TrimSpace(m.input.textarea.Value()) != "" {
			m.input.textarea.Reset()
			return m, nil
		}
		if m.verifyCancel != nil {
			m.verifyCancel()
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status", Raw: "(verify interrupted)",
			})
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
			m.viewport.HalfPageUp()
		}
		return m, nil
	case tea.KeyPgDown:
		if !m.s.transcript {
			m.viewport.HalfPageDown()
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
