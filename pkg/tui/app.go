package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/clipboard"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/planmode"
	promptpkg "github.com/ealeixandre/moa/pkg/prompt"
	"github.com/ealeixandre/moa/pkg/memory"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/tasks"
	"github.com/ealeixandre/moa/pkg/verify"
)

const renderInterval = 50 * time.Millisecond // ~20fps — sufficient for streaming text

// streamState tracks what the agent is doing.
type streamState int

const (
	stateIdle        streamState = iota
	stateStreaming               // between message_start → message_end
	stateToolRunning             // between tool_exec_start → tool_exec_end
)

// state holds mutable data that must not be copied by Bubble Tea's value semantics.
type state struct {
	blocks           []messageBlock // conversation history, raw content
	streamText       string         // current streaming assistant text
	thinkingText     string         // current thinking text
	streamCache      string         // cached glamour render of streamText (updated by renderTick)
	textMaterialized bool           // text was already materialized into blocks by ToolCallStreaming
	dirty            bool           // streamText changed since last render tick
	viewportDirty    bool           // blocks changed, viewport needs refresh on next tick
	running          bool           // agent is running (tick should continue)
	streamState      streamState
	activeTools      int                   // number of tool calls currently executing
	showThinking     bool                  // toggle thinking visibility (Ctrl+T)
	expanded         bool                  // toggle expanded tool results (Ctrl+E)
	initialized      bool                  // first WindowSizeMsg processed
	runGen           uint64                // set from bus.RunStarted; single source of truth is the bus
	cleanupOnce      sync.Once             // idempotent cleanup
	pendingStatus    string                // transient generic status shown in View(), never persisted
	pendingTimeline  *pendingTimelineEvent // live timeline event shown in View() until next send
	sessionCost      float64               // accumulated USD cost this session
	sessionInput     int                   // accumulated input tokens (for cache %)
	sessionCacheRead int                   // accumulated cache_read tokens
	runStartMsgCount int                   // message count at start of current run (for delta cost)
	asyncSubagents   int                   // running async subagent count (for status display)
	transcript       bool                  // true when in transcript mode (Ctrl+O)
	fullHistory      bool                  // true when Ctrl+E in transcript mode shows everything
	runStartBlockIdx int                   // block index at start of current run (patch boundary)
	pendingImage     []byte   // raw image bytes waiting to be sent with next message
	pendingImageMime string   // mime type of pending image
	queuedSteers     []string // steer messages waiting to be processed by the agent
	chromeCache      string   // cached bottom chrome string (built once per frame)
	chromeCacheDirty bool     // chrome needs rebuild
	viewportCache      string // cached viewport.View() output
	viewportCacheDirty bool   // viewport needs re-render
}

type pendingTimelineEvent struct {
	Text    string
	Message core.AgentMessage
}

// appModel is the root Bubble Tea model.
type pickerPurpose int

const (
	pickerForModelSwitch  pickerPurpose = iota
	pickerForReviewConfig
)

type appModel struct {
	// Pointer to mutable state — safe across Bubble Tea model copies
	s *state

	// Bus runtime — all interaction goes through this
	runtime  *bus.SessionRuntime
	eventCh  chan busEventMsg
	quit     chan struct{}
	unsubAll func()
	baseCtx  context.Context // parent context for signal cancellation

	// Components
	renderer       *renderer
	viewport       viewport.Model
	input          inputModel
	status         statusModel
	picker         pickerModel
	pickerPurpose  pickerPurpose
	thinkingPicker thinkingPicker
	branchPicker   branchPicker
	cmdPalette     cmdPalette
	filePicker     filePicker
	permPrompt     permissionPrompt
	askPrompt      askPrompt
	sessionBrowser sessionBrowser
	statusBar      *StatusLine

	// Session persistence
	sessionStore session.SessionStore
	session      *session.Session
	cwd          string

	// Display
	modelName string

	// Provider switching (for model picker — factory data only)
	scopedModels         map[string]bool
	onPinnedModelsChange func([]string) error

	// Verify
	verifyCancel context.CancelFunc

	// Settings menu
	settingsMenu settingsMenu

	// Plan mode (display-only state — all operations go through bus)
	planMenu               planMenu
	reviewGen              uint64
	reviewStreamCh         chan planReviewStreamMsg
	lastReviewResult       *planmode.ReviewResult
	lastMenuVariant        planMenuVariant
	pendingReviewFeedback  string
	postReviewMenuPending  bool // set before FinishPlanReview to suppress PlanModeChanged→OpenPostSubmit
	taskWidget            taskWidget
	taskWidgetMode        tasks.WidgetMode // TUI-local display preference

	// Prompt templates
	promptTemplates []promptpkg.Template

	// Memory
	memoryStore *memory.Store

	// Voice input
	voice voiceRecorder

	// Layout
	width  int
	height int
}

// Config configures the TUI. All fields are optional except Runtime.
type Config struct {
	Runtime               *bus.SessionRuntime         // required — the session bus runtime
	SessionStore          session.SessionStore        // persistence backend (nil = no persistence)
	Session               *session.Session            // session to resume (nil = fresh start)
	StartInSessionBrowser bool                        // open the session browser before entering chat
	CWD                   string                      // working directory for session metadata
	PinnedModels          []string                    // model IDs pre-pinned for Ctrl+P cycling
	OnPinnedModelsChange  func([]string) error        // called when the user changes pinned models
	PromptTemplates       []promptpkg.Template        // available prompt templates
	Transcriber           core.Transcriber            // speech-to-text for voice input (nil = disabled)
	MemoryStore           *memory.Store               // per-project memory store (nil = memory disabled)
}

// isStructuralBusEvent returns true for events that must not be dropped.
// Only text/thinking deltas and tool exec updates are lossy — everything else is structural.
func isStructuralBusEvent(event any) bool {
	switch event.(type) {
	case bus.TextDelta, bus.ThinkingDelta, bus.ToolExecUpdate, bus.ToolCallDelta:
		return false
	default:
		return true
	}
}

// New creates the TUI model. All interaction goes through the bus runtime.
func New(ctx context.Context, cfg Config) appModel {
	eventCh := make(chan busEventMsg, 1024)
	quit := make(chan struct{})

	// Single ordered subscription for all bus events.
	unsubAll := cfg.Runtime.Bus.SubscribeAll(func(event any) {
		if isStructuralBusEvent(event) {
			select {
			case eventCh <- busEventMsg{event: event}:
			case <-time.After(5 * time.Second):
				// structural event dropped — should not happen
			}
		} else {
			select {
			case eventCh <- busEventMsg{event: event}:
			default: // lossy for deltas
			}
		}
	})

	vp := viewport.New(0, 0)
	vp.MouseWheelEnabled = true
	vp.MouseWheelDelta = 3
	vp.KeyMap = viewport.KeyMap{}

	m := appModel{
		s:                    &state{showThinking: true},
		runtime:              cfg.Runtime,
		eventCh:              eventCh,
		quit:                 quit,
		unsubAll:             unsubAll,
		baseCtx:              ctx,
		renderer:             newRenderer(80),
		viewport:             vp,
		input:                newInput(),
		status:               newStatus(),
		picker:               newPicker(),
		sessionBrowser:       newSessionBrowser(),
		statusBar:            NewStatusLine(statusLineStyle),
		sessionStore:         cfg.SessionStore,
		session:              cfg.Session,
		cwd:                  cfg.CWD,
		scopedModels:         pinnedModelsToSet(cfg.PinnedModels),
		onPinnedModelsChange: cfg.OnPinnedModelsChange,
		promptTemplates:      cfg.PromptTemplates,
		memoryStore:          cfg.MemoryStore,
		voice:                voiceRecorder{transcriber: cfg.Transcriber},
	}
	m.filePicker.SetWorkDir(cfg.CWD)

	// Query initial state from bus for display.
	b := cfg.Runtime.Bus
	if model, err := bus.QueryTyped[bus.GetModel, core.Model](b, bus.GetModel{}); err == nil {
		name := model.Name
		if name == "" {
			name = model.ID
		}
		m.modelName = name
		m.statusBar.UpdateModelSegment(name)
	}
	if thinking, err := bus.QueryTyped[bus.GetThinkingLevel, string](b, bus.GetThinkingLevel{}); err == nil {
		m.statusBar.UpdateThinkingSegment(thinking)
	}
	if permMode, err := bus.QueryTyped[bus.GetPermissionMode, string](b, bus.GetPermissionMode{}); err == nil {
		m.statusBar.UpdatePermissionsSegment(permMode)
	}
	if pathInfo, err := bus.QueryTyped[bus.GetPathPolicy, bus.PathPolicyInfo](b, bus.GetPathPolicy{}); err == nil && pathInfo.WorkspaceRoot != "" {
		m.statusBar.UpdatePathScopeSegment(pathInfo.Scope)
	}
	m.statusBar.UpdateContextSegment(0)

	// Plan mode initial display.
	if planInfo, err := bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](b, bus.GetPlanMode{}); err == nil {
		if planInfo.Mode != "" && planInfo.Mode != "off" {
			m.statusBar.UpdatePlanSegment(planInfo.Mode)
		}
	}

	if cfg.StartInSessionBrowser {
		m.sessionBrowser.Open()
		m.input.SetEnabled(false)
	}

	return m
}

// --- Bubble Tea interface ---

func (m appModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.waitForBusEvent()}
	if m.sessionBrowser.active {
		cmds = append(cmds, m.loadSessionBrowser())
	}
	// Initial session display on first WindowSizeMsg handled in Update.
	return tea.Batch(cmds...)
}

// waitForBusEvent returns a Cmd that blocks until the next bus event.
func (m appModel) waitForBusEvent() tea.Cmd {
	ch := m.eventCh
	quit := m.quit
	return func() tea.Msg {
		select {
		case e, ok := <-ch:
			if !ok {
				return nil
			}
			return e
		case <-quit:
			return nil
		}
	}
}

// Update is the main message router.
func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Mark chrome dirty only for events that can change it.
	// High-frequency streaming events (busEventMsg, renderTickMsg) handle
	// chrome invalidation in their own paths or via updateViewport.
	switch msg.(type) {
	case busEventMsg, renderTickMsg:
		// These are the hot path during streaming. busEventMsg with TextDelta
		// doesn't affect chrome. renderTickMsg goes through updateViewport →
		// resizeViewport which rebuilds chrome unconditionally.
	default:
		m.s.chromeCacheDirty = true
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		sizeChanged := m.width != msg.Width || m.height != msg.Height
		m.width = msg.Width
		m.height = msg.Height
		m.renderer.SetWidth(msg.Width)
		m.input.SetWidth(msg.Width)
		m.status.SetWidth(msg.Width)
		if m.s.streamText != "" && sizeChanged {
			m.s.dirty = true
		}
		if !m.s.initialized {
			m.s.initialized = true
			if m.session != nil {
				// Use display messages from tree (full history including pre-compaction).
				// Falls back to agent messages if tree is empty.
				displayMsgs := m.displayMessages()
				if len(displayMsgs) > 0 {
					m.rebuildFromMessages(displayMsgs)
					m.refreshContextSegment()
				}
			}
			m.updateViewport()
			return m, nil
		}
		if sizeChanged && !m.s.transcript {
			m.updateViewport()
		}
		return m, nil

	case tea.MouseMsg:
		if !m.s.transcript && !m.sessionBrowser.active {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			m.s.viewportCacheDirty = true // scroll may have changed
			return m, cmd
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case busEventMsg:
		cmds := m.handleBusEvent(msg.event)
		allCmds := []tea.Cmd{m.waitForBusEvent()}
		allCmds = append(allCmds, cmds...)
		return m, tea.Batch(allCmds...)

	case agentSendErrorMsg:
		m.s.running = false
		m.s.streamState = stateIdle
		m.input.SetEnabled(true)
		m.status.SetText("")
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: msg.Err.Error()})
		m.updateViewport()
		return m, nil

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
		if m.s.running || m.reviewStreamCh != nil {
			return m, renderTick()
		}
		return m, nil

	case clearScreenDoneMsg:
		return m, nil

	case clearThinkingStatusMsg:
		if !m.s.running {
			m.status.SetText("")
		}
		return m, nil

	case planReviewStreamMsg:
		if msg.ReviewGen != m.reviewGen {
			return m, nil
		}
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolName == "plan_review" && !b.ToolDone {
				b.ToolResult += msg.Delta
				b.touch()
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

	case voiceResultMsg:
		return m.handleVoiceResult(msg)

	case compactResultMsg:
		m.s.running = false
		m.input.SetEnabled(true)
		m.status.SetText("")
		if msg.Err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: "Compaction failed: " + msg.Err.Error(),
			})
			m.updateViewport()
		}
		// Success display handled by CompactionEnded bus event.
		return m, nil

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

	case shellResultMsg:
		return m.handleShellResult(msg)

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
		return m, nil

	case pinnedModelsSavedMsg:
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

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// View renders the full alt-screen layout.
func (m appModel) View() string {
	if m.width == 0 {
		return "Loading..."
	}

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

	// Cache viewport.View() — it's expensive (lipgloss stringWidth per line).
	// Only re-render when content, scroll position, or size changed.
	if m.s.viewportCacheDirty || m.s.viewportCache == "" {
		m.s.viewportCache = m.viewport.View()
		m.s.viewportCacheDirty = false
	}

	botStr := m.bottomChrome()

	var sections []string
	sections = append(sections, m.s.viewportCache)
	if botStr != "" {
		sections = append(sections, botStr)
	}
	return strings.Join(sections, "\n")
}

// --- Key handling ---

func (m appModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.sessionBrowser.active {
		return m.handleSessionBrowserKey(msg)
	}

	if m.askPrompt.active {
		return m.handleAskKey(msg)
	}

	if m.permPrompt.active {
		return m.handlePermissionKey(msg)
	}

	if m.picker.active {
		return m.handlePickerKey(msg)
	}

	if m.thinkingPicker.active {
		return m.handleThinkingPickerKey(msg)
	}

	if m.branchPicker.active {
		return m.handleBranchPickerKey(msg)
	}

	if m.settingsMenu.active {
		return m.handleSettingsKey(msg.String())
	}

	if m.planMenu.active {
		switch msg.Type {
		case tea.KeyCtrlE, tea.KeyCtrlO:
			// fall through
		default:
			return m.handlePlanMenuKey(msg)
		}
	}

	if m.s.transcript {
		switch msg.Type {
		case tea.KeyCtrlO, tea.KeyCtrlE:
			// fall through
		case tea.KeyCtrlT:
			m.s.showThinking = !m.s.showThinking
			content := m.renderTranscriptBlocks(m.s.fullHistory)
			return m, tea.Sequence(clearScreen(), tea.Println(content))
		case tea.KeyCtrlC:
			if m.verifyCancel != nil {
				m.verifyCancel()
				return m, nil
			}
			if m.s.running {
				_ = m.runtime.Bus.Execute(bus.AbortRun{})
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
		if m.filePicker.active {
			m.filePicker.Close()
			return m, m.forceRepaint()
		}
		if m.cmdPalette.active {
			m.cmdPalette.Close()
			m.input.textarea.Reset()
			return m, m.forceRepaint()
		}
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
			_ = m.runtime.Bus.Execute(bus.AbortRun{})
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
		current, _ := bus.QueryTyped[bus.GetThinkingLevel, string](m.runtime.Bus, bus.GetThinkingLevel{})
		level := cycleThinkingLevel(current)
		_ = m.runtime.Bus.Execute(bus.SetThinking{Level: level})
		// ConfigChanged event updates status bar
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
		if m.s.running {
			return m, nil
		}
		current, _ := bus.QueryTyped[bus.GetPermissionMode, string](m.runtime.Bus, bus.GetPermissionMode{})
		var next string
		switch current {
		case "ask":
			next = "auto"
		case "auto":
			next = "yolo"
		default:
			next = "ask"
		}
		return m.handlePermissionsSwitch(next)

	case tea.KeyCtrlE:
		if m.s.transcript {
			m.s.fullHistory = !m.s.fullHistory
			content := m.renderTranscriptBlocks(m.s.fullHistory)
			return m, tea.Sequence(clearScreen(), tea.Println(content))
		}
		if len(m.s.blocks) == 0 {
			return m, nil
		}
		m.s.expanded = !m.s.expanded
		m.updateViewport()
		return m, nil

	case tea.KeyCtrlR:
		return m.handleVoiceToggle()

	case tea.KeyCtrlV:
		if m.s.running {
			return m, nil
		}
		return m, m.checkClipboardImage()

	case tea.KeyCtrlO:
		if m.s.transcript {
			m.s.transcript = false
			m.s.fullHistory = false
			m.recomputeInputEnabled()
			m.updateViewport()
			return m, tea.Batch(
				tea.EnterAltScreen,
				tea.EnableMouseCellMotion,
			)
		}
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
			break
		}
		if m.filePicker.active {
			if m.filePicker.SelectedIsDir() {
				m.navigateFilePicker()
				return m, m.forceRepaint()
			}
			selected := m.filePicker.Selected()
			m.filePicker.Close()
			if selected != "" {
				m.acceptFileMention(selected)
			}
			return m, m.forceRepaint()
		}
		if m.s.running {
			text := m.input.Submit()
			if text == "" {
				return m, nil
			}
			m.s.queuedSteers = append(m.s.queuedSteers, text)
			_ = m.runtime.Bus.Execute(bus.SteerAgent{Text: text})
			return m, nil
		}

		if m.cmdPalette.active {
			selected := m.cmdPalette.Selected()
			m.cmdPalette.Close()
			if selected != "" {
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
		if strings.HasPrefix(text, "!") {
			return m.handleShellEscape(text)
		}
		return m.startAgentRun(text)

	case tea.KeyPgUp:
		if !m.s.transcript {
			m.viewport.HalfPageUp()
			m.s.viewportCacheDirty = true
		}
		return m, nil
	case tea.KeyPgDown:
		if !m.s.transcript {
			m.viewport.HalfPageDown()
			m.s.viewportCacheDirty = true
		}
		return m, nil

	case tea.KeyTab:
		if m.filePicker.active {
			if m.filePicker.SelectedIsDir() {
				m.navigateFilePicker()
				return m, m.forceRepaint()
			}
			selected := m.filePicker.Selected()
			m.filePicker.Close()
			if selected != "" {
				m.acceptFileMention(selected)
			}
			return m, m.forceRepaint()
		}
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
		// Tab path completion when not in any picker.
		if !m.s.running {
			text := m.input.textarea.Value()
			cursorPos := m.input.CursorByteOffset()
			if newText, newCursor, ok := tabCompletePath(text, cursorPos, m.cwd); ok {
				m.input.textarea.SetValue(newText)
				_ = newCursor // SetValue resets cursor; move to end
				m.input.textarea.CursorEnd()
				return m, nil
			}
		}

	case tea.KeyUp:
		if m.filePicker.active {
			m.filePicker.MoveUp()
			return m, nil
		}
		if msg.Alt && len(m.s.queuedSteers) > 0 {
			// Alt+Up: pull queued steers back into the input for editing/cancelling.
			combined := strings.Join(m.s.queuedSteers, "\n")
			m.s.queuedSteers = nil
			current := m.input.textarea.Value()
			if current != "" {
				combined = current + "\n" + combined
			}
			m.input.textarea.SetValue(combined)
			m.input.textarea.CursorEnd()
			m.updateViewport()
			return m, nil
		}
		if m.cmdPalette.active {
			m.cmdPalette.MoveUp()
			return m, nil
		}
		if m.s.streamState == stateIdle && m.input.HistoryUp() {
			m.cmdPalette.Update(m.input.textarea.Value())
			return m, nil
		}

	case tea.KeyDown:
		if m.filePicker.active {
			m.filePicker.MoveDown()
			return m, nil
		}
		if m.cmdPalette.active {
			m.cmdPalette.MoveDown()
			return m, nil
		}
		if m.s.streamState == stateIdle && m.input.HistoryDown() {
			m.cmdPalette.Update(m.input.textarea.Value())
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.s.streamState == stateIdle {
		val := m.input.textarea.Value()
		m.cmdPalette.Update(val)
		if !m.cmdPalette.active {
			m.filePicker.Update(val, m.input.CursorByteOffset())
		} else {
			m.filePicker.Close()
		}
	}
	return m, cmd
}

// acceptFileMention replaces the @filter token in the input with the selected file path.
func (m *appModel) acceptFileMention(path string) {
	text := m.input.textarea.Value()
	cursorPos := m.input.CursorByteOffset()

	// Find the @ that started this mention (walk backwards from cursor).
	atIdx := -1
	for i := cursorPos - 1; i >= 0; i-- {
		if text[i] == '@' {
			atIdx = i
			break
		}
		if text[i] == ' ' || text[i] == '\t' || text[i] == '\n' || text[i] == '\r' {
			break
		}
	}

	// Add trailing space unless path ends with / (navigating into directory).
	suffix := " "
	if strings.HasSuffix(path, "/") {
		suffix = ""
	}

	if atIdx < 0 {
		// Fallback: just append.
		m.input.textarea.SetValue(text + path + suffix)
		m.input.textarea.CursorEnd()
		return
	}

	// Keep @ when navigating into dirs so the picker stays active.
	// Remove @ when accepting a final file.
	prefix := ""
	if strings.HasSuffix(path, "/") {
		prefix = "@"
	}
	newText := text[:atIdx] + prefix + path + suffix + text[cursorPos:]
	m.input.textarea.SetValue(newText)
	m.input.textarea.CursorEnd()
}

// navigateFilePicker enters a selected directory: updates the @token to include
// the directory path and re-triggers the file picker to show its contents.
func (m *appModel) navigateFilePicker() {
	selected := m.filePicker.Selected()
	if selected == "" {
		return
	}
	// Replace the @filter with @dir/ so the picker re-opens showing dir contents.
	m.acceptFileMention(selected + "/")

	// Re-trigger the file picker with the updated text.
	val := m.input.textarea.Value()
	m.filePicker.Update(val, m.input.CursorByteOffset())
}

// --- Bus event handling ---

func (m *appModel) handleBusEvent(event any) []tea.Cmd {
	// Mark chrome dirty for events that affect status/chrome.
	// Skip the three high-frequency streaming events that never change chrome.
	switch event.(type) {
	case bus.TextDelta, bus.ThinkingDelta, bus.ToolExecUpdate, bus.ToolCallDelta:
		// no-op: these don't affect status bar, input, or other chrome
	default:
		m.s.chromeCacheDirty = true
	}
	switch e := event.(type) {
	// --- Streaming ---
	case bus.TextDelta:
		if e.RunGen != m.s.runGen {
			return nil
		}
		m.s.streamText += e.Delta
		m.s.dirty = true

	case bus.ThinkingDelta:
		if e.RunGen != m.s.runGen {
			return nil
		}
		m.s.thinkingText += e.Delta
		m.s.dirty = true

	case bus.MessageStarted:
		if e.RunGen != m.s.runGen {
			return nil
		}
		m.s.streamText = ""
		m.s.thinkingText = ""
		m.s.streamCache = ""
		m.s.textMaterialized = false
		m.s.streamState = stateStreaming
		m.status.SetText("generating...")

	case bus.MessageEnded:
		if e.RunGen != m.s.runGen {
			return nil
		}
		if m.s.thinkingText != "" {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "thinking", Raw: m.s.thinkingText,
			})
		}
		assistantText := m.s.streamText
		if assistantText == "" && !m.s.textMaterialized {
			// Only fall back to FullText if no text was already materialized
			// by ToolCallStreaming during this message.
			assistantText = e.FullText
		}
		if assistantText != "" {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "assistant", Raw: assistantText,
			})
		}
		m.s.streamText = ""
		m.s.thinkingText = ""
		m.s.streamCache = ""
		m.s.textMaterialized = false
		m.s.viewportDirty = true

	case bus.ToolCallStreaming:
		if e.RunGen != m.s.runGen {
			return nil
		}
		// Materialize any pending stream text BEFORE the tool block
		// to maintain correct chronological order.
		if m.s.thinkingText != "" {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "thinking", Raw: m.s.thinkingText,
			})
			m.s.thinkingText = ""
			m.s.textMaterialized = true
		}
		if m.s.streamText != "" {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "assistant", Raw: m.s.streamText,
			})
			m.s.streamText = ""
			m.s.textMaterialized = true
		}
		m.s.streamCache = ""
		m.s.streamState = stateToolRunning
		m.status.SetText("generating " + e.ToolName + "...")
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type:       "tool",
			ToolCallID: e.ToolCallID,
			ToolName:   e.ToolName,
			Generating: true,
		})
		m.s.viewportDirty = true

	case bus.ToolCallDelta:
		if e.RunGen != m.s.runGen {
			return nil
		}
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolCallID == e.ToolCallID {
				b.ToolArgs = e.Args
				b.touch()
				m.s.viewportDirty = true
				break
			}
		}

	case bus.ToolExecStarted:
		if e.RunGen != m.s.runGen {
			return nil
		}
		m.s.activeTools++
		m.s.streamState = stateToolRunning
		if m.s.activeTools == 1 {
			m.status.SetText("running " + e.ToolName + "...")
		} else {
			m.status.SetText(fmt.Sprintf("running %d tools...", m.s.activeTools))
		}
		// Check if block was already created by ToolCallStreaming
		found := false
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolCallID == e.ToolCallID {
				b.ToolArgs = e.Args // authoritative final args
				b.Generating = false
				b.touch()
				found = true
				break
			}
		}
		if !found {
			// Fallback: create block (ToolCallStreaming was missed or not emitted)
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "tool", ToolCallID: e.ToolCallID, ToolName: e.ToolName, ToolArgs: e.Args,
			})
		}
		m.s.viewportDirty = true

	case bus.ToolExecUpdate:
		if e.RunGen != m.s.runGen {
			return nil
		}
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolCallID == e.ToolCallID {
				if b.ToolName == "edit" {
					b.ToolDiff = e.Delta
				} else {
					b.ToolResult += e.Delta
				}
				b.touch()
				m.s.viewportDirty = true
				break
			}
		}

	case bus.ToolExecEnded:
		if e.RunGen != m.s.runGen {
			return nil
		}
		m.s.activeTools--
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolCallID == e.ToolCallID {
				b.ToolDone = true
				b.IsError = e.IsError
				b.Rejected = e.Rejected
				b.ToolResult = e.Result
				b.ToolNote = extractToolNote(b.ToolResult, b.Rejected)
				b.touch()
				break
			}
		}
		// Invalidate file picker cache after successful file edits.
		if !e.IsError && !e.Rejected {
			switch e.ToolName {
			case "edit", "write", "multiedit", "apply_patch":
				m.filePicker.Invalidate()
			}
		}
		m.s.viewportDirty = true
		if m.s.activeTools <= 0 {
			m.s.activeTools = 0
			m.s.streamState = stateStreaming
			m.status.SetText("generating...")
		} else if m.s.activeTools == 1 {
			m.status.SetText("running tool...")
		} else {
			m.status.SetText(fmt.Sprintf("running %d tools...", m.s.activeTools))
		}
		if e.ToolName == "tasks" {
			m.refreshTaskDisplay()
		}

	case bus.Steered:
		if e.RunGen != m.s.runGen {
			return nil
		}
		for i, s := range m.s.queuedSteers {
			if s == e.Text {
				m.s.queuedSteers = append(m.s.queuedSteers[:i], m.s.queuedSteers[i+1:]...)
				break
			}
		}
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

	case bus.CompactionStarted:
		m.status.SetText("compacting context...")

	case bus.CompactionEnded:
		if e.Err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status",
				Raw:  "⚠ Compaction failed: " + e.Err.Error() + " (continuing with full context)",
			})
		} else if e.Payload != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status",
				Raw:  fmt.Sprintf("✂ Context compacted (%dK → %dK tokens)", e.Payload.TokensBefore/1000, e.Payload.TokensAfter/1000),
			})
		}
		m.status.SetText("generating...")
		m.refreshContextSegment()
		m.s.viewportDirty = true

	// --- Run lifecycle ---
	case bus.RunStarted:
		m.s.runGen = e.RunGen

	case bus.RunEnded:
		if e.RunGen != m.s.runGen {
			return nil
		}
		return m.handleRunEnded(e)

	// --- Auto-verify ---
	case bus.AutoVerifyStarted:
		m.status.SetText("running auto-verify...")
		return nil

	case bus.AutoVerifyEnded:
		if e.Err != nil {
			m.status.SetText("auto-verify: " + e.Err.Error())
			return []tea.Cmd{tea.Tick(3*time.Second, func(time.Time) tea.Msg { return clearThinkingStatusMsg{} })}
		}
		if e.AllPass {
			m.status.SetText("✓ auto-verify passed")
			return []tea.Cmd{tea.Tick(2*time.Second, func(time.Time) tea.Msg { return clearThinkingStatusMsg{} })}
		}
		m.status.SetText("✗ auto-verify failed — sending to agent...")
		return nil

	// --- Permissions ---
	case bus.PermissionRequested:
		return m.handlePermissionRequested(e)

	case bus.PermissionResolved:
		// no-op for TUI

	// --- Ask user ---
	case bus.AskUserRequested:
		return m.handleAskUserRequested(e)

	case bus.AskUserResolved:
		// no-op

	// --- Config ---
	case bus.ConfigChanged:
		if e.Model != "" {
			m.modelName = e.Model
			m.statusBar.UpdateModelSegment(e.Model)
		}
		if e.Thinking != "" {
			m.statusBar.UpdateThinkingSegment(e.Thinking)
		}
		if e.PermissionMode != "" {
			m.statusBar.UpdatePermissionsSegment(e.PermissionMode)
		}
		if e.PathScope != "" {
			m.statusBar.UpdatePathScopeSegment(e.PathScope)
		}

	// --- Plan mode ---
	case bus.PlanModeChanged:
		return m.handlePlanModeChanged(e)

	// --- Tasks ---
	case bus.TasksUpdated:
		m.refreshTaskDisplay()

	// --- Context ---
	case bus.ContextUpdated:
		m.statusBar.UpdateContextSegment(e.Percent)

	// --- Subagents ---
	case bus.SubagentCountChanged:
		m.s.asyncSubagents = e.Count

	case bus.SubagentCompleted:
		return m.handleSubagentCompleted(e)
	}
	return nil
}

func (m *appModel) handlePermissionRequested(e bus.PermissionRequested) []tea.Cmd {
	var cmds []tea.Cmd
	if m.s.transcript {
		m.s.transcript = false
		m.s.fullHistory = false
		m.updateViewport()
		cmds = append(cmds, tea.EnterAltScreen, tea.EnableMouseCellMotion)
	}
	permMode, _ := bus.QueryTyped[bus.GetPermissionMode, string](m.runtime.Bus, bus.GetPermissionMode{})
	m.permPrompt.ShowFromBus(e.ID, e.ToolName, e.Args, e.AllowPattern, permMode)
	return cmds
}

func (m *appModel) handleAskUserRequested(e bus.AskUserRequested) []tea.Cmd {
	var cmds []tea.Cmd
	if m.s.transcript {
		m.s.transcript = false
		m.s.fullHistory = false
		m.updateViewport()
		cmds = append(cmds, tea.EnterAltScreen, tea.EnableMouseCellMotion)
	}
	m.askPrompt.ShowFromBus(e.ID, e.Questions)
	return cmds
}

func (m *appModel) handlePlanModeChanged(e bus.PlanModeChanged) []tea.Cmd {
	if e.Mode == "" || e.Mode == "off" {
		m.statusBar.UpdatePlanSegment("")
		m.statusBar.UpdateTasksSegment(0, 0)
	} else {
		m.statusBar.UpdatePlanSegment(e.Mode)
	}
	// Plan submitted → show action menu, but NOT if we just finished a review
	// (the review result handler opens the appropriate post-review menu).
	if e.Mode == "ready" && !m.postReviewMenuPending {
		m.planMenu.OpenPostSubmit()
		m.lastMenuVariant = menuPostSubmit
		m.input.SetEnabled(false)
	}
	m.postReviewMenuPending = false
	return nil
}

func (m *appModel) handleSubagentCompleted(e bus.SubagentCompleted) []tea.Cmd {
	if m.s.running {
		// Agent is mid-run — inject as steer.
		_ = m.runtime.Bus.Execute(bus.SteerAgent{Text: e.Text})
		return nil
	}
	// Agent is idle — start a notification run.
	return m.startSubagentNotificationRun(e)
}

// startSubagentNotificationRun starts an agent run triggered by a subagent completion.
func (m *appModel) startSubagentNotificationRun(e bus.SubagentCompleted) []tea.Cmd {
	if err := m.commitPendingTimelineEvent(); err != nil {
		m.s.pendingStatus = "✗ " + err.Error()
		return nil
	}

	m.s.pendingStatus = ""
	m.s.blocks = append(m.s.blocks, messageBlock{
		Type:           "subagent",
		SubagentTask:   e.Task,
		SubagentStatus: e.Status,
		SubagentResult: e.Text,
	})

	m.prepareRun(truncateLabel(e.Task))
	m.updateViewport()

	b := m.runtime.Bus
	custom := map[string]any{
		"source":          "subagent",
		"subagent_job_id": e.JobID,
		"subagent_task":   e.Task,
		"subagent_status": e.Status,
	}
	cmd := func() tea.Msg {
		err := b.Execute(bus.SendPrompt{Text: e.Text, Custom: custom})
		if err != nil {
			return agentSendErrorMsg{Err: err}
		}
		return nil
	}
	return []tea.Cmd{cmd, renderTick(), m.status.spinner.Tick}
}

// handleRunEnded processes the completion of an agent run.
func (m *appModel) handleRunEnded(e bus.RunEnded) []tea.Cmd {
	// No gen bump needed — RunStarted sets the gen for the next run.
	// Events are ordered through eventCh, so no late events arrive after RunEnded.

	// Materialize any in-flight stream content into blocks.
	// This happens when the run was interrupted before MessageEnded arrived.
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

	// Clean up any tool blocks still in Generating state (run aborted before execution).
	for i := range m.s.blocks {
		if m.s.blocks[i].Type == "tool" && m.s.blocks[i].Generating {
			m.s.blocks[i].Generating = false
			m.s.blocks[i].ToolDone = true
			m.s.blocks[i].IsError = true
			m.s.blocks[i].ToolResult = "Run ended before tool execution started"
			m.s.blocks[i].touch()
		}
	}

	// Query messages for reconciliation.
	msgs := m.currentMessages()
	m.patchFromMessages(msgs)

	m.s.running = false
	m.s.streamState = stateIdle
	m.s.streamText = ""
	m.s.thinkingText = ""
	m.s.streamCache = ""
	for _, s := range m.s.queuedSteers {
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "user", Raw: s})
	}
	m.s.queuedSteers = nil
	m.status.SetText("")
	m.input.textarea.Placeholder = "Ask anything... (Ctrl+J for newline)"
	m.input.SetEnabled(true)
	m.refreshContextSegment()
	m.accumulateCost(msgs)

	if e.Err != nil {
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "error", Raw: "Error: " + e.Err.Error(),
		})
	}

	// Plan mode: check if all tasks done during execution → auto-exit.
	if planInfo, err := bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](m.runtime.Bus, bus.GetPlanMode{}); err == nil {
		if planInfo.Mode == "executing" {
			if taskList, err := bus.QueryTyped[bus.GetTasks, []tasks.Task](m.runtime.Bus, bus.GetTasks{}); err == nil {
				allDone := len(taskList) > 0
				for _, t := range taskList {
					if t.Status != "done" {
						allDone = false
						break
					}
				}
				if allDone {
					_ = m.runtime.Bus.Execute(bus.ExitPlanMode{})
					m.s.blocks = append(m.s.blocks, messageBlock{
						Type: "status", Raw: "✅ All tasks complete — plan mode finished",
					})
				}
			}
		}
	}

	m.refreshTaskDisplay()
	m.updateViewport()
	return nil
}

// --- Helpers ---

// currentMessages queries the current conversation from the bus (for LLM context).
func (m *appModel) currentMessages() []core.AgentMessage {
	msgs, _ := bus.QueryTyped[bus.GetMessages, []core.AgentMessage](m.runtime.Bus, bus.GetMessages{})
	return msgs
}

// displayMessages returns the full message history for display (from tree).
// Falls back to currentMessages if tree is not available.
func (m *appModel) displayMessages() []core.AgentMessage {
	msgs, err := bus.QueryTyped[bus.GetDisplayMessages, []core.AgentMessage](m.runtime.Bus, bus.GetDisplayMessages{})
	if err != nil || len(msgs) == 0 {
		return m.currentMessages()
	}
	return msgs
}

func (m appModel) forceRepaint() tea.Cmd {
	w, h := m.width, m.height
	return func() tea.Msg {
		return tea.WindowSizeMsg{Width: w, Height: h}
	}
}

func renderTick() tea.Cmd {
	return tea.Tick(renderInterval, func(time.Time) tea.Msg {
		return renderTickMsg{}
	})
}

func (m *appModel) cleanup() {
	m.s.cleanupOnce.Do(func() {
		close(m.quit)
		if m.unsubAll != nil {
			m.unsubAll()
		}
		m.runtime.Close()
		m.voice.reset()
	})
}

// refreshContextSegment recalculates the context usage percentage.
func (m *appModel) refreshContextSegment() {
	if pct, err := bus.QueryTyped[bus.GetContextUsage, int](m.runtime.Bus, bus.GetContextUsage{}); err == nil {
		if pct < 0 {
			m.statusBar.Remove(SegmentContext)
		} else {
			m.statusBar.UpdateContextSegment(pct)
		}
	}
}

// accumulateCost sums Usage from new assistant messages.
func (m *appModel) accumulateCost(msgs []core.AgentMessage) {
	if msgs == nil {
		return
	}
	model, _ := bus.QueryTyped[bus.GetModel, core.Model](m.runtime.Bus, bus.GetModel{})
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
			m.s.sessionInput += msg.Usage.Input
			m.s.sessionCacheRead += msg.Usage.CacheRead
		}
	}
	m.statusBar.UpdateCostSegment(m.s.sessionCost)
	totalInput := m.s.sessionInput + m.s.sessionCacheRead
	if totalInput > 0 {
		pct := m.s.sessionCacheRead * 100 / totalInput
		m.statusBar.UpdateCacheSegment(pct)
	}
}

// refreshTaskDisplay updates the task progress in the status bar.
func (m *appModel) refreshTaskDisplay() {
	taskList, err := bus.QueryTyped[bus.GetTasks, []tasks.Task](m.runtime.Bus, bus.GetTasks{})
	if err != nil {
		return
	}
	done := 0
	for _, t := range taskList {
		if t.Status == "done" {
			done++
		}
	}
	total := len(taskList)
	m.statusBar.UpdateTasksSegment(done, total)
	if planInfo, err := bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](m.runtime.Bus, bus.GetPlanMode{}); err == nil {
		if planInfo.Mode == "executing" {
			if total > 0 {
				m.statusBar.UpdatePlanSegment(fmt.Sprintf("executing 📋 %d/%d", done, total))
			} else {
				m.statusBar.UpdatePlanSegment("executing")
			}
		}
	}
	m.s.viewportDirty = true
}
