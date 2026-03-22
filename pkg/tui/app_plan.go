package tui

import (
	"fmt"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/planmode"
)

// --- Plan mode ---

func (m appModel) handlePlanCommand() (tea.Model, tea.Cmd) {
	info, _ := bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](m.runtime.Bus, bus.GetPlanMode{})
	if info.Mode == "" || info.Mode == "off" {
		// Enter plan mode.
		if m.s.running {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Cannot toggle plan mode while agent is running"})
			return m, nil
		}
		if err := m.runtime.Bus.Execute(bus.EnterPlanMode{}); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Plan mode: " + err.Error()})
			return m, nil
		}
		// PlanModeChanged event updates status bar.
		// Re-query for plan file path.
		info, _ = bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](m.runtime.Bus, bus.GetPlanMode{})
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status",
			Raw:  "📋 Plan mode enabled — plan file: " + info.PlanFile,
		})
		m.updateViewport()
		return m, nil
	}

	switch info.Mode {
	case "ready":
		m.planMenu.OpenPostSubmit()
		m.lastMenuVariant = menuPostSubmit
		m.input.SetEnabled(false)
		m.updateViewport()
		return m, nil

	case "planning":
		if m.s.running {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Cannot toggle plan mode while agent is running"})
			return m, nil
		}
		_ = m.runtime.Bus.Execute(bus.ExitPlanMode{})
		// PlanModeChanged event updates status bar.
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: "Plan mode disabled"})
		m.updateViewport()
		return m, nil

	case "executing":
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Plan is currently executing — wait for it to finish or use /plan after completion"})
		m.updateViewport()
		return m, nil

	case "reviewing":
		m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: "Plan review in progress"})
		m.updateViewport()
		return m, nil
	}

	return m, nil
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
		actions := m.planMenu.actions()
		if m.planMenu.cursor < len(actions) && actions[m.planMenu.cursor].action == planActionReview {
			m.planMenu.Close()
			model, _ := bus.QueryTyped[bus.GetModel, core.Model](m.runtime.Bus, bus.GetModel{})
			m.picker.Open(model.ID, m.scopedModels)
			m.pickerPurpose = pickerForReviewConfig
		}
	}
	return m, nil
}

func (m appModel) executePlanAction(action planAction) (tea.Model, tea.Cmd) {
	b := m.runtime.Bus

	switch action {
	case planActionExecuteClean:
		if err := b.Execute(bus.StartPlanExecution{CleanContext: true}); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: err.Error()})
			return m, nil
		}
		m.s.blocks = m.s.blocks[:0]
		m.s.sessionCost = 0
		m.s.sessionInput = 0
		m.s.sessionCacheRead = 0
		m.statusBar.UpdateCostSegment(0)
		m.statusBar.UpdateCacheSegment(0)
		info, _ := bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](b, bus.GetPlanMode{})
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status", Raw: "▶ Executing plan (clean context)",
		})
		m.updateViewport()
		return m.sendMessage("Execute the plan saved at " + info.PlanFile + ". Read the plan file first, then create tasks and begin implementing.")

	case planActionExecuteKeep:
		if err := b.Execute(bus.StartPlanExecution{CleanContext: false}); err != nil {
			m.s.blocks = append(m.s.blocks, messageBlock{Type: "error", Raw: err.Error()})
			return m, nil
		}
		info, _ := bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](b, bus.GetPlanMode{})
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status", Raw: "▶ Executing plan (keeping context)",
		})
		m.updateViewport()
		return m.sendMessage("Execute the plan saved at " + info.PlanFile + ". Read the plan file first, then create tasks and begin implementing.")

	case planActionReview:
		_ = b.Execute(bus.StartPlanReview{})
		m.input.SetEnabled(false)
		m.status.SetText("reviewing plan...")
		// Build review args for the tool block display.
		info, _ := bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](b, bus.GetPlanMode{})
		reviewArgs := map[string]any{"plan": info.PlanFile}
		if m.planMenu.reviewModel != "" {
			reviewArgs["model"] = m.planMenu.reviewModel
		}
		if m.planMenu.reviewThinking != "" {
			reviewArgs["thinking"] = m.planMenu.reviewThinking
		}
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
		_ = b.Execute(bus.ContinueRefining{})
		// PlanModeChanged event updates status bar.
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status", Raw: "✏️ Continuing to refine plan",
		})
		m.updateViewport()
		return m, nil

	case planActionEditor:
		info, _ := bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](b, bus.GetPlanMode{})
		m.lastMenuVariant = m.planMenu.variant
		return m, openInEditor(info.PlanFile)

	case planActionAutoRefine:
		_ = b.Execute(bus.ContinueRefining{})
		feedback := ""
		if m.lastReviewResult != nil {
			feedback = m.lastReviewResult.Feedback
		}
		msg := "The reviewer found issues with your plan. Address the feedback and resubmit with `submit_plan`:\n\n" + feedback
		return m.sendMessage(msg)

	case planActionRefineWithOwn:
		_ = b.Execute(bus.ContinueRefining{})
		m.input.SetEnabled(true)
		feedback := ""
		if m.lastReviewResult != nil {
			feedback = m.lastReviewResult.Feedback
		}
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status",
			Raw:  "✏️ Type your instructions. They'll be sent along with the reviewer feedback.",
		})
		m.pendingReviewFeedback = feedback
		m.updateViewport()
		return m, nil

	case planActionExecAnywayClean:
		return m.executePlanAction(planActionExecuteClean)

	case planActionExecAnywayKeep:
		return m.executePlanAction(planActionExecuteKeep)

	case planActionStayInPlanMode:
		_ = b.Execute(bus.ContinueRefining{})
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
// Input stays enabled so the user can steer mid-run (same as startAgentRun).
func (m appModel) sendMessage(text string) (tea.Model, tea.Cmd) {
	m.s.blocks = append(m.s.blocks, messageBlock{Type: "user", Raw: text})
	m.prepareRun(truncateLabel(text))
	m.updateViewport()
	return m, m.launchAgentSend(text)
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
	ch := make(chan planReviewStreamMsg, 64)
	m.reviewStreamCh = ch
	info, _ := bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](m.runtime.Bus, bus.GetPlanMode{})
	planPath := info.PlanFile

	// Build review config from PlanMode via the runtime's SessionContext.
	// The plan mode's GetReviewConfig already has provider factory and tools.
	sctx := m.runtime.Context()
	var reviewCfg planmode.ReviewConfig
	if sctx != nil && sctx.PlanMode != nil {
		reviewCfg = sctx.PlanMode.GetReviewConfig()
	}
	reviewCfg.ThinkingLevel = info.ReviewThinkingLevel

	// Resolve review model.
	reviewModelID := info.ReviewModelID
	if m.planMenu.reviewModelID != "" {
		reviewModelID = m.planMenu.reviewModelID
	}
	if reviewModelID != "" {
		resolved, _ := core.ResolveModel(reviewModelID)
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

func (m appModel) waitForReviewStream() tea.Cmd {
	ch := m.reviewStreamCh
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m appModel) handlePlanReviewResult(msg planReviewResultMsg) (tea.Model, tea.Cmd) {
	if msg.ReviewGen != m.reviewGen {
		return m, nil
	}
	planInfo, _ := bus.QueryTyped[bus.GetPlanMode, bus.PlanModeInfo](m.runtime.Bus, bus.GetPlanMode{})
	if planInfo.Mode != "reviewing" {
		return m, nil
	}
	// Set flag BEFORE FinishPlanReview so that the resulting PlanModeChanged("ready")
	// event doesn't override our post-review menu with the generic post-submit menu.
	m.postReviewMenuPending = true
	_ = m.runtime.Bus.Execute(bus.FinishPlanReview{})
	m.status.SetText("")
	m.reviewStreamCh = nil

	if msg.Err != nil {
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolName == "plan_review" && !b.ToolDone {
				b.ToolDone = true
				b.IsError = true
				b.ToolResult = "Review failed: " + msg.Err.Error()
				b.touch()
				break
			}
		}
		m.planMenu.OpenPostSubmit()
		m.lastMenuVariant = menuPostSubmit
	} else {
		for i := len(m.s.blocks) - 1; i >= 0; i-- {
			b := &m.s.blocks[i]
			if b.Type == "tool" && b.ToolName == "plan_review" && !b.ToolDone {
				b.ToolDone = true
				b.ToolResult = msg.Result.Feedback
				b.touch()
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

// commitPendingTimelineEvent appends the pending timeline event to the conversation via bus.
func (m *appModel) commitPendingTimelineEvent() error {
	if m.s.pendingTimeline == nil {
		return nil
	}
	err := m.runtime.Bus.Execute(bus.AppendToConversation{Message: m.s.pendingTimeline.Message})
	if err != nil {
		return err
	}
	m.s.blocks = append(m.s.blocks, messageBlock{Type: "status", Raw: m.s.pendingTimeline.Text})
	m.s.pendingTimeline = nil
	return nil
}
