package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/tasks"
	"github.com/ealeixandre/moa/pkg/verify"
)

// --- Commands ---

func (m appModel) handleCommand(cmd string) (tea.Model, tea.Cmd) {
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
	if cmd == "prompt" || strings.HasPrefix(cmd, "prompt ") {
		return m.handlePromptTemplate(strings.TrimSpace(strings.TrimPrefix(cmd, "prompt")))
	}
	if cmd == "path" || strings.HasPrefix(cmd, "path ") {
		return m.handlePathCommand(strings.TrimSpace(strings.TrimPrefix(cmd, "path")))
	}
	if cmd == "memory" || strings.HasPrefix(cmd, "memory ") {
		return m.handleMemoryCommand(strings.TrimSpace(strings.TrimPrefix(cmd, "memory")))
	}

	switch cmd {
	case "model", "models":
		model, _ := bus.QueryTyped[bus.GetModel, core.Model](m.runtime.Bus, bus.GetModel{})
		m.picker.Open(model.ID, m.scopedModels)
		m.input.SetEnabled(false)
		return m, nil

	case "thinking":
		thinking, _ := bus.QueryTyped[bus.GetThinkingLevel, string](m.runtime.Bus, bus.GetThinkingLevel{})
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: "thinking: " + thinking})
		m.updateViewport()
		return m, nil

	case "permissions":
		info, _ := bus.QueryTyped[bus.GetPermissionInfo, bus.PermissionInfo](m.runtime.Bus, bus.GetPermissionInfo{})
		text := "permissions: " + info.Mode
		if len(info.AllowPatterns) > 0 {
			text += "\nallow: " + strings.Join(info.AllowPatterns, ", ")
		}
		if len(info.Rules) > 0 {
			text += "\nrules: " + strings.Join(info.Rules, ", ")
		}
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: text})
		m.updateViewport()
		return m, nil

	case "clear":
		if err := m.runtime.Bus.Execute(bus.ClearSession{}); err != nil {
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
		m.s.sessionInput = 0
		m.s.sessionCacheRead = 0
		m.statusBar.UpdateCostSegment(0)
		m.statusBar.UpdateCacheSegment(0)
		m.s.expanded = false
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
		b := m.runtime.Bus
		return m, func() tea.Msg {
			err := b.Execute(bus.CompactSession{})
			return compactResultMsg{Err: err}
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

	case "settings":
		return m.openSettingsMenu()

	case "voice":
		return m.handleVoiceToggle()

	case "undo":
		if m.s.running {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: "Cannot undo while agent is running",
			})
			return m, nil
		}
		if err := m.runtime.Bus.Execute(bus.UndoLastChange{}); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: err.Error(),
			})
			m.updateViewport()
			return m, nil
		}
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: "⏪ Undo completed"})
		m.updateViewport()
		return m, nil

	case "branch", "back":
		if m.s.running {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: "Cannot branch while agent is running",
			})
			return m, nil
		}
		return m.handleBranchCommand()

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

// handleAskKey routes keys to the ask prompt.
func (m appModel) handleAskKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		m.askPrompt.CursorUp()
		return m, nil
	case tea.KeyDown:
		m.askPrompt.CursorDown()
		return m, nil
	case tea.KeyEnter:
		if m.askPrompt.Submit() {
			// All questions answered — resolve via bus.
			_ = m.runtime.Bus.Execute(bus.ResolveAskUser{
				AskID:   m.askPrompt.askID,
				Answers: m.askPrompt.CollectAnswers(),
			})
			return m, nil
		}
		return m, nil
	case tea.KeyShiftTab:
		m.askPrompt.Back()
		return m, nil
	case tea.KeyBackspace:
		m.askPrompt.Backspace()
		return m, nil
	case tea.KeyEsc:
		askID := m.askPrompt.askID
		// Send empty answers (one per question) so the tool doesn't hang.
		emptyAnswers := make([]string, len(m.askPrompt.questions))
		m.askPrompt.Cancel()
		if err := m.runtime.Bus.Execute(bus.ResolveAskUser{
			AskID:   askID,
			Answers: emptyAnswers,
		}); err != nil {
			m.s.pendingStatus = "✗ " + err.Error()
		}
		return m, nil
	case tea.KeyRunes, tea.KeySpace:
		s := msg.String()
		if len(s) == 1 && s[0] >= '1' && s[0] <= '9' && !m.askPrompt.isCustom() {
			idx := int(s[0] - '1')
			if idx < m.askPrompt.optionCount() {
				m.askPrompt.cursor = idx
				return m, nil
			}
		}
		for _, r := range s {
			m.askPrompt.TypeRune(r)
		}
		return m, nil
	}
	return m, nil
}

func (m appModel) handlePermissionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Rule input mode (auto mode, option "Add rule")
	if m.permPrompt.ruleMode {
		switch msg.Type {
		case tea.KeyEnter:
			if rule := m.permPrompt.SaveRule(); rule != "" {
				_ = m.runtime.Bus.Execute(bus.AddPermissionRule{
					PermissionID: m.permPrompt.permID,
					Rule:         rule,
				})
				m.s.blocks = append(m.s.blocks, messageBlock{
					Type: "status", Raw: fmt.Sprintf("✓ rule added: %s", rule),
				})
			}
			return m, nil // stay on prompt
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
			opt := m.permPrompt.options[m.permPrompt.cursor]
			_ = m.runtime.Bus.Execute(bus.ResolvePermission{
				PermissionID: m.permPrompt.permID,
				Approved:     opt.approved,
				Feedback:     strings.TrimSpace(m.permPrompt.amendBuf),
				AllowPattern: opt.allow,
			})
			m.permPrompt.active = false
			return m, nil
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
		_ = m.runtime.Bus.Execute(bus.ResolvePermission{
			PermissionID: m.permPrompt.permID,
			Approved:     opt.approved,
			Feedback:     strings.TrimSpace(m.permPrompt.amendBuf),
			AllowPattern: opt.allow,
		})
		m.permPrompt.active = false
		return m, nil
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
		_ = m.runtime.Bus.Execute(bus.ResolvePermission{
			PermissionID: m.permPrompt.permID,
			Approved:     false,
		})
		m.permPrompt.active = false
		_ = m.runtime.Bus.Execute(bus.AbortRun{})
		return m, nil
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
				_ = m.runtime.Bus.Execute(bus.ResolvePermission{
					PermissionID: m.permPrompt.permID,
					Approved:     opt.approved,
					AllowPattern: opt.allow,
				})
				m.permPrompt.active = false
				return m, nil
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

func (m appModel) handleThinkingPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.thinkingPicker.Close()
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

func (m appModel) handleBranchPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.branchPicker.Close()
		m.updateViewport()
		return m, nil

	case tea.KeyUp:
		m.branchPicker.MoveUp()
	case tea.KeyDown:
		m.branchPicker.MoveDown()

	case tea.KeyEnter:
		selected := m.branchPicker.Selected()
		m.branchPicker.Close()
		if selected.EntryID != "" {
			return m.executeBranch(selected.EntryID)
		}
		return m, nil

	case tea.KeyRunes:
		switch string(msg.Runes) {
		case "j":
			m.branchPicker.MoveDown()
		case "k":
			m.branchPicker.MoveUp()
		}
	}
	m.updateViewport()
	return m, nil
}

// switchToModel performs the actual model switch via bus.
func (m appModel) switchToModel(newModel core.Model) (tea.Model, tea.Cmd) {
	spec := newModel.ID
	if newModel.Provider != "" {
		spec = newModel.Provider + "/" + newModel.ID
	}
	if err := m.runtime.Bus.Execute(bus.SwitchModel{ModelSpec: spec}); err != nil {
		m.s.pendingStatus = "✗ " + err.Error()
		m.s.pendingTimeline = nil
		return m, nil
	}
	// Query new model for display.
	model, _ := bus.QueryTyped[bus.GetModel, core.Model](m.runtime.Bus, bus.GetModel{})
	name := model.Name
	if name == "" {
		name = model.ID
	}
	m.modelName = name
	m.refreshContextSegment()
	m.s.pendingStatus = ""
	m.s.pendingTimeline = newModelSwitchEvent(model)
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

	currentModel, _ := bus.QueryTyped[bus.GetModel, core.Model](m.runtime.Bus, bus.GetModel{})
	currentID := currentModel.ID
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
	if err := m.runtime.Bus.Execute(bus.SetPermissionMode{Mode: modeStr}); err != nil {
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "error", Raw: err.Error(),
		})
		m.updateViewport()
		return m, nil
	}
	// ConfigChanged event handles status bar update.
	m.s.blocks = append(m.s.blocks, messageBlock{
		Type: "status", Raw: "permissions: " + modeStr,
	})
	m.updateViewport()
	return m, nil
}

// handlePathCommand processes `/path` and its subcommands.
func (m appModel) handlePathCommand(args string) (tea.Model, tea.Cmd) {
	b := m.runtime.Bus
	pathInfo, _ := bus.QueryTyped[bus.GetPathPolicy, bus.PathPolicyInfo](b, bus.GetPathPolicy{})
	if pathInfo.WorkspaceRoot == "" {
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "error", Raw: "Path policy not available",
		})
		m.updateViewport()
		return m, nil
	}

	sub := args
	var subArg string
	if idx := strings.IndexByte(args, ' '); idx >= 0 {
		sub = args[:idx]
		subArg = strings.TrimSpace(args[idx+1:])
	}

	switch sub {
	case "", "list":
		info := fmt.Sprintf("path scope: %s\nworkspace: %s", pathInfo.Scope, pathInfo.WorkspaceRoot)
		if len(pathInfo.AllowedPaths) > 0 {
			info += "\nallowed paths:\n  " + strings.Join(pathInfo.AllowedPaths, "\n  ")
		}
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: info})

	case "add":
		if subArg == "" {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: "Usage: /path add <directory>",
			})
			m.updateViewport()
			return m, nil
		}
		if err := b.Execute(bus.AddAllowedPath{Path: subArg}); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: fmt.Sprintf("Cannot add path: %v", err),
			})
			m.updateViewport()
			return m, nil
		}
		// ConfigChanged event updates PathScope in status bar.
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status", Raw: fmt.Sprintf("✓ Path added: %s", subArg),
		})

	case "rm", "remove":
		if subArg == "" {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: "Usage: /path rm <directory>",
			})
			m.updateViewport()
			return m, nil
		}
		if err := b.Execute(bus.RemoveAllowedPath{Path: subArg}); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: err.Error(),
			})
		} else {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status", Raw: fmt.Sprintf("✓ Path removed: %s", subArg),
			})
		}

	case "scope":
		if err := b.Execute(bus.SetPathScope{Scope: subArg}); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "error", Raw: err.Error(),
			})
		} else {
			m.s.blocks = append(m.s.blocks, messageBlock{
				Type: "status", Raw: "✓ Path scope: " + subArg,
			})
		}

	default:
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "error", Raw: "Unknown subcommand: /path " + sub + "\nUsage: /path [list|add <dir>|rm <dir>|scope workspace|unrestricted]",
		})
	}

	m.updateViewport()
	return m, nil
}

// handleMemoryCommand processes `/memory [edit|clear|path]`.
func (m appModel) handleMemoryCommand(subcmd string) (tea.Model, tea.Cmd) {
	if m.memoryStore == nil {
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "error", Raw: "Memory is disabled. Set \"memory_enabled\": true in config.",
		})
		m.updateViewport()
		return m, nil
	}

	cwd := m.cwd

	switch subcmd {
	case "":
		content, err := m.memoryStore.Load(cwd)
		if err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: err.Error()})
			m.updateViewport()
			return m, nil
		}
		if content == "" {
			content = "No memory saved for this project yet."
		}
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: "📝 Project Memory:\n\n" + content})
		m.updateViewport()
		return m, nil

	case "edit":
		path := m.memoryStore.FilePath(cwd)
		// Ensure file exists so editor can open it.
		if _, err := os.Stat(path); os.IsNotExist(err) {
			_ = m.memoryStore.Save(cwd, "# Project Memory\n\n")
		}
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		//nolint:gosec // editor is user-controlled by design
		c := exec.Command(editor, path)
		return m, tea.ExecProcess(c, func(err error) tea.Msg {
			return editorDoneMsg{err: err}
		})

	case "clear":
		if err := m.memoryStore.Save(cwd, ""); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: err.Error()})
			m.updateViewport()
			return m, nil
		}
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: "🗑️ Project memory cleared."})
		m.updateViewport()
		return m, nil

	case "path":
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status", Raw: m.memoryStore.FilePath(cwd),
		})
		m.updateViewport()
		return m, nil

	default:
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "error", Raw: "Usage: /memory [edit|clear|path]",
		})
		m.updateViewport()
		return m, nil
	}
}

// handleThinkingSwitch processes `/thinking <level>`.
func (m appModel) handleThinkingSwitch(level string) (tea.Model, tea.Cmd) {
	if m.s.running {
		m.s.pendingStatus = "✗ Cannot change thinking while agent is running"
		return m, nil
	}
	if err := m.runtime.Bus.Execute(bus.SetThinking{Level: level}); err != nil {
		m.s.pendingStatus = "✗ " + err.Error()
		return m, nil
	}
	// ConfigChanged event updates status bar.
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
	// Runtime metadata is set via the persistence reactor.
	return sess
}

func (m appModel) activateSession(sess *session.Session) (tea.Model, tea.Cmd) {
	// TODO: In full implementation, switchRuntime would create a new runtime.
	// For now, use bus commands to load the session state.
	if sess == nil {
		sess = m.newSession()
		_ = m.runtime.Bus.Execute(bus.ClearSession{})
	} else if len(sess.Messages) > 0 {
		// Load messages into agent via bus. This is temporary —
		// full session switching creates a new runtime.
		// For now, we approximate with ClearSession + metadata restore.
		_ = m.runtime.Bus.Execute(bus.ClearSession{})
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
	m.s.sessionInput = 0
	m.s.sessionCacheRead = 0
	m.statusBar.UpdateCostSegment(0)
	m.statusBar.UpdateCacheSegment(0)

	// Restore metadata via bus commands.
	if sess != nil && sess.Metadata != nil {
		if modelSpec, ok := sess.Metadata["model"].(string); ok && modelSpec != "" {
			_ = m.runtime.Bus.Execute(bus.SwitchModel{ModelSpec: modelSpec})
		}
		if thinking, ok := sess.Metadata["thinking"].(string); ok && thinking != "" {
			_ = m.runtime.Bus.Execute(bus.SetThinking{Level: thinking})
		}
		if mode, ok := sess.Metadata["permission_mode"].(string); ok && mode != "" {
			_ = m.runtime.Bus.Execute(bus.SetPermissionMode{Mode: mode})
		}
	}

	// Query updated state for display.
	if model, err := bus.QueryTyped[bus.GetModel, core.Model](m.runtime.Bus, bus.GetModel{}); err == nil {
		name := model.Name
		if name == "" {
			name = model.ID
		}
		m.modelName = name
		m.statusBar.UpdateModelSegment(name)
	}
	if thinking, err := bus.QueryTyped[bus.GetThinkingLevel, string](m.runtime.Bus, bus.GetThinkingLevel{}); err == nil {
		m.statusBar.UpdateThinkingSegment(thinking)
	}
	if permMode, err := bus.QueryTyped[bus.GetPermissionMode, string](m.runtime.Bus, bus.GetPermissionMode{}); err == nil {
		m.statusBar.UpdatePermissionsSegment(permMode)
	}

	// Use display messages from tree for full history (includes pre-compaction messages).
	// Fall back to session.Messages for v1 sessions or when tree is empty (e.g., during
	// activateSession which clears and reloads state — the tree may not be populated yet).
	displayMsgs := m.displayMessages()
	if len(displayMsgs) == 0 && sess != nil {
		displayMsgs = sess.Messages
	}
	if len(displayMsgs) > 0 {
		m.rebuildFromMessages(displayMsgs)
	}
	m.refreshContextSegment()
	m.refreshTaskDisplay()
	m.updateViewport()
	return m, m.forceRepaint()
}

// handlePromptTemplate processes `/prompt` and `/prompt <name>`.
func (m appModel) handlePromptTemplate(name string) (tea.Model, tea.Cmd) {
	if len(m.promptTemplates) == 0 {
		m.status.SetText("no prompt templates found in .moa/prompts/ or ~/.config/moa/prompts/")
		return m, nil
	}

	if name == "" {
		var lines []string
		for _, t := range m.promptTemplates {
			entry := t.Name
			if len(t.Placeholders) > 0 {
				entry += " ({{" + strings.Join(t.Placeholders, "}}, {{") + "}})"
			}
			lines = append(lines, "  "+entry)
		}
		msg := "available templates:\n" + strings.Join(lines, "\n")
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: msg})
		m.updateViewport()
		return m, nil
	}

	for _, t := range m.promptTemplates {
		if t.Name == name {
			m.input.textarea.SetValue(t.Content)
			m.input.textarea.CursorEnd()
			return m, nil
		}
	}

	m.status.SetText("unknown template: " + name)
	return m, nil
}

// openSettingsMenu builds entries from current state via bus queries.
func (m appModel) openSettingsMenu() (tea.Model, tea.Cmd) {
	permMode, _ := bus.QueryTyped[bus.GetPermissionMode, string](m.runtime.Bus, bus.GetPermissionMode{})
	thinking, _ := bus.QueryTyped[bus.GetThinkingLevel, string](m.runtime.Bus, bus.GetThinkingLevel{})
	model, _ := bus.QueryTyped[bus.GetModel, core.Model](m.runtime.Bus, bus.GetModel{})

	entries := []settingsEntry{
		{key: "Model", value: model.Name, options: nil},
		{key: "Thinking", value: thinking, options: []string{"off", "minimal", "low", "medium", "high"}},
		{key: "Permissions", value: permMode, options: []string{"yolo", "ask", "auto"}},
	}

	m.settingsMenu.Open(entries)
	m.input.SetEnabled(false)
	return m, nil
}

func (m appModel) handleSettingsKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "up", "k":
		m.settingsMenu.MoveUp()
	case "down", "j":
		m.settingsMenu.MoveDown()
	case "enter", "right", "l", "tab":
		return m.applySettingsCycle()
	case "left", "h":
		return m.applySettingsCycleReverse()
	case "esc", "q":
		m.settingsMenu.Close()
		m.input.SetEnabled(true)
	}
	return m, nil
}

func (m appModel) applySettingsCycle() (tea.Model, tea.Cmd) {
	key, val := m.settingsMenu.CycleValue()
	return m.applySettingsChange(key, val)
}

func (m appModel) applySettingsCycleReverse() (tea.Model, tea.Cmd) {
	if !m.settingsMenu.active || m.settingsMenu.cursor >= len(m.settingsMenu.entries) {
		return m, nil
	}
	e := &m.settingsMenu.entries[m.settingsMenu.cursor]
	if len(e.options) == 0 {
		return m, nil
	}
	idx := len(e.options) - 1
	for i, opt := range e.options {
		if opt == e.value {
			idx = (i - 1 + len(e.options)) % len(e.options)
			break
		}
	}
	e.value = e.options[idx]
	return m.applySettingsChange(e.key, e.value)
}

func (m appModel) applySettingsChange(key, val string) (tea.Model, tea.Cmd) {
	if key == "" {
		return m, nil
	}
	switch key {
	case "Thinking":
		return m.handleThinkingSwitch(val)
	case "Permissions":
		return m.handlePermissionsSwitch(val)
	}
	return m, nil
}

// handleShellEscape dispatches a shell command asynchronously.
func (m appModel) handleShellEscape(text string) (tea.Model, tea.Cmd) {
	silent := strings.HasPrefix(text, "!!")
	var command string
	if silent {
		command = strings.TrimSpace(text[2:])
	} else {
		command = strings.TrimSpace(text[1:])
	}
	if command == "" {
		return m, nil
	}

	m.s.blocks = append(m.s.blocks, messageBlock{
		Type:     "tool",
		ToolName: "bash",
		ToolArgs: map[string]any{"command": command},
	})
	m.s.viewportDirty = true
	m.updateViewport()

	b := m.runtime.Bus
	running := m.s.running
	cwd := m.cwd
	baseCtx := m.baseCtx

	return m, func() tea.Msg {
		cmd := exec.CommandContext(baseCtx, "sh", "-c", command)
		cmd.Dir = cwd
		out, _ := cmd.CombinedOutput()
		output := strings.TrimRight(string(out), "\n")
		isError := cmd.ProcessState != nil && !cmd.ProcessState.Success()
		_ = b // available if needed for steer
		return shellResultMsg{
			Command: command,
			Output:  output,
			IsError: isError,
			Silent:  silent,
			Running: running,
		}
	}
}

// handleShellResult processes the completed shell escape command.
func (m appModel) handleShellResult(msg shellResultMsg) (tea.Model, tea.Cmd) {
	b := m.runtime.Bus
	if msg.Running && !msg.Silent {
		body := fmt.Sprintf("Shell output (from user):\n$ %s\n%s", msg.Command, msg.Output)
		_ = b.Execute(bus.SteerAgent{Text: body})
	} else if !msg.Running {
		var body string
		if msg.Output != "" {
			body = fmt.Sprintf("$ %s\n%s", msg.Command, msg.Output)
		} else {
			body = fmt.Sprintf("$ %s\n(no output)", msg.Command)
		}
		role := "user"
		if msg.Silent {
			role = "shell"
		}
		agentMsg := core.AgentMessage{
			Message: core.Message{
				Role:    role,
				Content: []core.Content{core.TextContent(body)},
			},
			Custom: map[string]any{"shell": true},
		}
		_ = b.Execute(bus.AppendToConversation{Message: agentMsg})
	}

	for i := len(m.s.blocks) - 1; i >= 0; i-- {
		blk := &m.s.blocks[i]
		if blk.ToolName == "bash" && !blk.ToolDone {
			if cmd, _ := blk.ToolArgs["command"].(string); cmd == msg.Command {
				blk.ToolResult = msg.Output
				blk.ToolDone = true
				blk.IsError = msg.IsError
				blk.touch()
				break
			}
		}
	}

	m.s.viewportDirty = true
	m.updateViewport()
	return m, nil
}

// handleTasksCommand processes `/tasks` and its subcommands.
func (m appModel) handleTasksCommand(args string) (tea.Model, tea.Cmd) {
	b := m.runtime.Bus
	taskList, err := bus.QueryTyped[bus.GetTasks, []tasks.Task](b, bus.GetTasks{})
	if err != nil {
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Task tracking not available"})
		return m, nil
	}

	parts := strings.Fields(args)

	if len(parts) == 0 {
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
		if err := b.Execute(bus.MarkTaskDone{TaskID: id}); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: err.Error()})
			m.updateViewport()
			return m, nil
		}
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: fmt.Sprintf("✅ Task #%d marked done", id)})
		m.refreshTaskDisplay()
		m.updateViewport()
		return m, nil

	case "reset":
		_ = b.Execute(bus.ResetTasks{})
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: "Tasks cleared"})
		m.refreshTaskDisplay()
		m.updateViewport()
		return m, nil

	case "show":
		if len(parts) >= 2 {
			switch tasks.WidgetMode(parts[1]) {
			case tasks.WidgetAll, tasks.WidgetCurrent, tasks.WidgetHidden:
				m.taskWidgetMode = tasks.WidgetMode(parts[1])
			default:
				m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Usage: /tasks show [all|current|hidden]"})
				m.updateViewport()
				return m, nil
			}
		} else {
			switch m.taskWidgetMode {
			case tasks.WidgetAll:
				m.taskWidgetMode = tasks.WidgetCurrent
			case tasks.WidgetCurrent:
				m.taskWidgetMode = tasks.WidgetHidden
			default:
				m.taskWidgetMode = tasks.WidgetAll
			}
		}
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: fmt.Sprintf("Task widget: %s", m.taskWidgetMode)})
		m.updateViewport()
		return m, nil

	default:
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Usage: /tasks [done <n> | reset | show [all|current|hidden]]"})
		m.updateViewport()
		return m, nil
	}
}

// handleBranchCommand opens the branch picker.
func (m appModel) handleBranchCommand() (appModel, tea.Cmd) {
	points, err := bus.QueryTyped[bus.GetBranchPoints, []bus.BranchPoint](m.runtime.Bus, bus.GetBranchPoints{})
	if err != nil || len(points) == 0 {
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "No branch points available"})
		m.updateViewport()
		return m, nil
	}
	m.branchPicker.Open(points)
	m.updateViewport()
	return m, nil
}

// executeBranch moves the conversation to the selected branch point.
func (m appModel) executeBranch(entryID string) (appModel, tea.Cmd) {
	if err := m.runtime.Bus.Execute(bus.BranchTo{EntryID: entryID}); err != nil {
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Branch failed: " + err.Error()})
		m.updateViewport()
		return m, nil
	}

	// Rebuild display from tree
	m.s.blocks = nil
	displayMsgs := m.displayMessages()
	if len(displayMsgs) > 0 {
		m.rebuildFromMessages(displayMsgs)
	}
	m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: "🌿 Branched — new conversation path started"})
	m.refreshContextSegment()
	m.updateViewport()
	return m, nil
}
