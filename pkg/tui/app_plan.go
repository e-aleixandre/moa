package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/planmode"
	"github.com/ealeixandre/moa/pkg/tasks"
)

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
		m.statusBar.UpdatePlanSegment("")
		m.statusBar.UpdateTasksSegment(0, 0)
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
	m.statusBar.UpdatePlanSegment("planning")
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
				return &core.ToolCallDecision{Block: true, Reason: reason, Kind: core.ToolCallDecisionKindPolicy}
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
		m.s.sessionInput = 0
		m.s.sessionCacheRead = 0
		m.statusBar.UpdateCostSegment(0)
		m.statusBar.UpdateCacheSegment(0)
		m.planMode.StartExecution()
		m.syncPermissionCheck()
		m.rebuildSystemPrompt()
		m.statusBar.UpdatePlanSegment("executing")
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
		m.statusBar.UpdatePlanSegment("executing")
		planFile := m.planMode.PlanFilePath()
		m.s.blocks = append(m.s.blocks, messageBlock{
			Type: "status", Raw: "▶ Executing plan (keeping context)",
		})
		m.updateViewport()
		return m.sendMessage("Execute the plan saved at " + planFile + ". Read the plan file first, then create tasks and begin implementing.")

	case planActionReview:
		m.planMode.StartReview()
		m.statusBar.UpdatePlanSegment("reviewing")
		m.input.SetEnabled(false)
		m.status.SetText("reviewing plan...")
		// Build review args for the tool block display.
		reviewArgs := map[string]any{"plan": m.planMode.PlanFilePath()}
		var modelLabel string
		if m.planMenu.reviewModel != "" {
			modelLabel = m.planMenu.reviewModel
			reviewArgs["model"] = modelLabel
		}
		var thinkingLabel string
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
		m.statusBar.UpdatePlanSegment("planning")
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
		m.statusBar.UpdatePlanSegment("planning")
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
		m.statusBar.UpdatePlanSegment("planning")
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
		m.statusBar.UpdatePlanSegment("planning")
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
	gen := m.prepareRun(truncateLabel(text))
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
	m.statusBar.UpdatePlanSegment("ready")
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
