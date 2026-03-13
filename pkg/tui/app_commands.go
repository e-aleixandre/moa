package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/planmode"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/verify"
)

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

	case "verify":
		if m.s.running {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: "Cannot verify while agent is running",
			})
			return m, nil
		}
		m.s.running = true
		m.input.SetEnabled(false)
		m.status.SetText("running verify checks...")
		cwd := m.cwd
		ctx, cancel := context.WithCancel(m.baseCtx)
		m.verifyCancel = cancel
		return m, func() tea.Msg {
			defer cancel()
			result, err := verify.Execute(ctx, cwd)
			if err != nil {
				return verifyResultMsg{Err: err}
			}
			return verifyResultMsg{Result: &result}
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
