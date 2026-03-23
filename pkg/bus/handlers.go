package bus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/planmode"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/tasks"
	"github.com/ealeixandre/moa/pkg/verify"
)

// rebuildSystemPrompt updates the agent's system prompt based on the current
// plan mode state. Called by plan mode handlers after state transitions.
func rebuildSystemPrompt(sctx *SessionContext) {
	if sctx.PlanMode == nil {
		return
	}
	prompt := sctx.BaseSystemPrompt
	mode := sctx.PlanMode.Mode()
	planFile := sctx.PlanMode.PlanFilePath()
	if mode == planmode.ModePlanning {
		if p := planmode.PlanningPrompt(planFile); p != "" {
			prompt += "\n\n" + p
		}
	}
	if mode == planmode.ModeExecuting {
		if p := planmode.ExecutionPrompt(planFile); p != "" {
			prompt += "\n\n" + p
		}
	}
	_ = sctx.Agent.SetSystemPrompt(prompt)
}

// RegisterHandlers registers command and query handlers for a session on its bus.
// Call once after creating a SessionContext.
func RegisterHandlers(sctx *SessionContext) {
	b := sctx.Bus

	// -------------------------------------------------------------------
	// Commands
	// -------------------------------------------------------------------

	b.OnCommand(func(cmd AbortRun) error {
		// Cancel run context FIRST so runCtx.Err() != nil before Agent.Abort()
		// causes runFn to return. This prevents misclassifying abort as real error.
		sctx.cancelRun()
		sctx.Agent.Abort()
		return nil
	})

	b.OnCommand(func(cmd SteerAgent) error {
		sctx.Agent.Steer(cmd.Text)
		return nil
	})

	b.OnCommand(func(cmd SwitchModel) error {
		if sctx.ProviderFactory == nil {
			return fmt.Errorf("model switching unavailable: provider factory not configured")
		}
		newModel, ok := core.ResolveModel(cmd.ModelSpec)
		if !ok {
			return fmt.Errorf("unknown model: %s", cmd.ModelSpec)
		}
		newProvider, err := sctx.ProviderFactory(newModel)
		if err != nil {
			return fmt.Errorf("provider error: %w", err)
		}
		if err := sctx.Agent.SetModel(newProvider, newModel); err != nil {
			return err
		}
		sctx.Bus.Publish(ConfigChanged{
			SessionID: sctx.SessionID,
			Model:     newModel.Name,
			Thinking:  sctx.Agent.ThinkingLevel(),
		})
		return nil
	})

	b.OnCommand(func(cmd SetThinking) error {
		valid := map[string]bool{"off": true, "minimal": true, "low": true, "medium": true, "high": true}
		if !valid[cmd.Level] {
			return fmt.Errorf("invalid thinking level %q (options: off, minimal, low, medium, high)", cmd.Level)
		}
		if err := sctx.Agent.SetThinkingLevel(cmd.Level); err != nil {
			return err
		}
		sctx.Bus.Publish(ConfigChanged{
			SessionID: sctx.SessionID,
			Thinking:  cmd.Level,
		})
		return nil
	})

	b.OnCommand(func(cmd ClearSession) error {
		if err := sctx.Agent.Reset(); err != nil {
			return err
		}
		// If we were in error state, transition back to idle.
		if sctx.State != nil && sctx.State.Current() == StateError {
			_ = sctx.State.Transition(StateIdle)
		}
		sctx.Bus.Publish(CommandExecuted{
			SessionID: sctx.SessionID,
			Command:   "clear",
		})
		return nil
	})

	b.OnCommand(func(cmd CompactSession) error {
		// Emit CompactionStarted/Ended explicitly (agent.Compact doesn't emit lifecycle events).
		sctx.Bus.Publish(CompactionStarted{SessionID: sctx.SessionID})
		result, err := sctx.Agent.Compact(sctx.SessionCtx)
		if err != nil {
			sctx.Bus.Publish(CompactionEnded{SessionID: sctx.SessionID, Err: err})
			return err
		}
		// Signal compaction ended (with or without payload).
		sctx.Bus.Publish(CompactionEnded{
			SessionID: sctx.SessionID,
			Payload:   result, // nil if nothing to compact
		})
		// Always publish CommandExecuted on success so persistence and frontends react.
		sctx.Bus.Publish(CommandExecuted{
			SessionID: sctx.SessionID,
			Command:   "compact",
			Messages:  sctx.Agent.Messages(),
		})
		return nil
	})

	b.OnCommand(func(cmd UndoLastChange) error {
		if sctx.Checkpoints == nil {
			return fmt.Errorf("checkpoints not available")
		}
		cp, err := sctx.Checkpoints.Undo()
		if err != nil {
			return err
		}
		var restoreErrs []string
		for _, snap := range cp.Files {
			if snap.Content == nil {
				// File was created by the agent — delete it.
				if rmErr := os.Remove(snap.Path); rmErr != nil && !os.IsNotExist(rmErr) {
					restoreErrs = append(restoreErrs, fmt.Sprintf("delete %s: %v", filepath.Base(snap.Path), rmErr))
				}
			} else {
				// File existed before — restore original content.
				if wErr := os.WriteFile(snap.Path, snap.Content, snap.Perm); wErr != nil {
					restoreErrs = append(restoreErrs, fmt.Sprintf("restore %s: %v", filepath.Base(snap.Path), wErr))
				}
			}
		}
		if len(restoreErrs) > 0 {
			// Push checkpoint back so undo can be retried.
			sctx.Checkpoints.Repush(cp)
			return fmt.Errorf("partial restore: %s", strings.Join(restoreErrs, "; "))
		}
		return nil
	})

	b.OnCommand(func(cmd MarkTaskDone) error {
		if sctx.TaskStore == nil {
			return fmt.Errorf("task store not available")
		}
		if err := sctx.TaskStore.MarkDoneErr(cmd.TaskID); err != nil {
			return err
		}
		sctx.Bus.Publish(TasksUpdated{
			SessionID: sctx.SessionID,
			Tasks:     sctx.TaskStore.Tasks(),
		})
		return nil
	})

	b.OnCommand(func(cmd ResetTasks) error {
		if sctx.TaskStore == nil {
			return fmt.Errorf("task store not available")
		}
		sctx.TaskStore.Reset()
		sctx.Bus.Publish(TasksUpdated{
			SessionID: sctx.SessionID,
			Tasks:     sctx.TaskStore.Tasks(),
		})
		return nil
	})

	// -------------------------------------------------------------------
	// Queries
	// -------------------------------------------------------------------

	b.OnQuery(func(q GetMessages) ([]core.AgentMessage, error) {
		return sctx.Agent.Messages(), nil
	})

	b.OnQuery(func(q GetModel) (core.Model, error) {
		return sctx.Agent.Model(), nil
	})

	b.OnQuery(func(q GetThinkingLevel) (string, error) {
		return sctx.Agent.ThinkingLevel(), nil
	})

	b.OnQuery(func(q GetContextUsage) (int, error) {
		model := sctx.Agent.Model()
		if model.MaxInput <= 0 {
			return -1, nil
		}
		msgs := sctx.Agent.Messages()
		est := core.EstimateContextTokens(msgs, "", nil, sctx.Agent.CompactionEpoch())
		pct := (est.Tokens * 100) / model.MaxInput
		if pct > 100 {
			pct = 100
		}
		return pct, nil
	})

	b.OnQuery(func(q GetTasks) ([]tasks.Task, error) {
		if sctx.TaskStore == nil {
			return nil, nil
		}
		return sctx.TaskStore.Tasks(), nil
	})

	b.OnQuery(func(q GetPlanMode) (PlanModeInfo, error) {
		if sctx.PlanMode == nil {
			return PlanModeInfo{Mode: "off"}, nil
		}
		rc := sctx.PlanMode.GetReviewConfig()
		reviewName := rc.Model.Name
		if reviewName == "" {
			reviewName = rc.Model.ID
		}
		return PlanModeInfo{
			Mode:                string(sctx.PlanMode.Mode()),
			PlanFile:            sctx.PlanMode.PlanFilePath(),
			ReviewModelID:       rc.Model.ID,
			ReviewModelName:     reviewName,
			ReviewThinkingLevel: rc.ThinkingLevel,
		}, nil
	})

	b.OnQuery(func(q GetCompactionEpoch) (int, error) {
		return sctx.Agent.CompactionEpoch(), nil
	})

	b.OnQuery(func(q GetPermissionMode) (string, error) {
		if g := sctx.GetGate(); g != nil {
			return string(g.Mode()), nil
		}
		return "yolo", nil
	})

	b.OnQuery(func(q GetPermissionInfo) (PermissionInfo, error) {
		g := sctx.GetGate()
		if g == nil {
			return PermissionInfo{Mode: "yolo"}, nil
		}
		return PermissionInfo{
			Mode:          string(g.Mode()),
			AllowPatterns: g.AllowPatterns(),
			Rules:         g.Rules(),
		}, nil
	})

	b.OnQuery(func(q GetPathPolicy) (PathPolicyInfo, error) {
		if sctx.PathPolicy == nil {
			return PathPolicyInfo{}, nil
		}
		return PathPolicyInfo{
			WorkspaceRoot: sctx.PathPolicy.WorkspaceRoot(),
			Scope:         sctx.PathPolicy.Scope(),
			AllowedPaths:  sctx.PathPolicy.AllowedPaths(),
		}, nil
	})

	// GetSessionState returns the current state.
	// Note: "permission" state is defined but not wired in this phase.
	// Permission bridges (Gate.Requests → PermissionRequested) remain
	// in serve/TUI until Fase 2b.
	b.OnQuery(func(q GetSessionState) (string, error) {
		if sctx.State == nil {
			return "idle", nil
		}
		return string(sctx.State.Current()), nil
	})

	// -------------------------------------------------------------------
	// Agent run commands (async — spawn goroutine)
	// -------------------------------------------------------------------

	// Auto-verify state: retry counter + cancel function for in-flight verify.
	var autoVerifyCount atomic.Int32
	var autoVerifyCancel atomic.Pointer[context.CancelFunc]

	// cancelAutoVerify cancels any in-flight auto-verify goroutine.
	cancelAutoVerify := func() {
		if fn := autoVerifyCancel.Swap(nil); fn != nil {
			(*fn)()
		}
	}

	b.OnCommand(func(cmd SendPrompt) error {
		// Reset auto-verify counter on user-initiated prompts.
		if cmd.Custom == nil || cmd.Custom["source"] != "auto_verify" {
			autoVerifyCount.Store(0)
			cancelAutoVerify()
		}
		return startRun(sctx, cmd.Text, func(ctx context.Context) ([]core.AgentMessage, error) {
			if cmd.Custom != nil {
				return sctx.Agent.SendWithCustom(ctx, cmd.Text, cmd.Custom)
			}
			return sctx.Agent.Send(ctx, cmd.Text)
		})
	})

	b.OnCommand(func(cmd SendPromptWithContent) error {
		// User-initiated content send resets auto-verify counter.
		autoVerifyCount.Store(0)
		cancelAutoVerify()
		label := "content"
		if len(cmd.Content) > 0 && cmd.Content[0].Text != "" {
			label = cmd.Content[0].Text
		}
		return startRun(sctx, label, func(ctx context.Context) ([]core.AgentMessage, error) {
			return sctx.Agent.SendWithContent(ctx, cmd.Content)
		})
	})

	// -------------------------------------------------------------------
	// AppendToConversation
	// -------------------------------------------------------------------

	b.OnCommand(func(cmd AppendToConversation) error {
		return sctx.Agent.AppendMessage(cmd.Message)
	})

	// -------------------------------------------------------------------
	// Permission management
	// -------------------------------------------------------------------

	b.OnCommand(func(cmd SetPermissionMode) error {
		valid := map[string]permission.Mode{
			"yolo": permission.ModeYolo,
			"ask":  permission.ModeAsk,
			"auto": permission.ModeAuto,
		}
		newMode, ok := valid[strings.ToLower(cmd.Mode)]
		if !ok {
			return fmt.Errorf("invalid permission mode %q (options: yolo, ask, auto)", cmd.Mode)
		}

		if newMode == permission.ModeYolo {
			if sctx.Approvals != nil {
				sctx.Approvals.StopPermissionBridge()
			}
			sctx.SetGate(nil)
			if sctx.PathPolicy != nil {
				sctx.PathPolicy.SetUnrestricted(true)
			}
		} else if sctx.GetGate() == nil {
			// Reconstruct gate with preserved config (allow/deny/rules/headless).
			g := permission.New(newMode, sctx.GateConfig)
			sctx.SetGate(g)
			if sctx.Approvals != nil {
				sctx.Approvals.StartPermissionBridge(sctx.SessionCtx, g)
			}
		} else {
			sctx.GetGate().SetMode(newMode)
		}

		var modeStr string
		if g := sctx.GetGate(); g != nil {
			modeStr = string(g.Mode())
		} else {
			modeStr = "yolo"
		}
		evt := ConfigChanged{
			SessionID:      sctx.SessionID,
			PermissionMode: modeStr,
		}
		// If path policy was changed (yolo → unrestricted), include it.
		if sctx.PathPolicy != nil {
			evt.PathScope = sctx.PathPolicy.Scope()
		}
		sctx.Bus.Publish(evt)
		return nil
	})

	b.OnCommand(func(cmd ResolvePermission) error {
		if sctx.Approvals == nil {
			return fmt.Errorf("approvals not available")
		}
		return sctx.Approvals.ResolvePermission(cmd.PermissionID, cmd.Approved, cmd.Feedback, cmd.AllowPattern)
	})

	b.OnCommand(func(cmd AddPermissionRule) error {
		g := sctx.GetGate()
		if g == nil {
			return fmt.Errorf("no permission gate active")
		}
		if sctx.Approvals == nil {
			return fmt.Errorf("approvals not available")
		}
		if err := sctx.Approvals.ValidatePending(cmd.PermissionID); err != nil {
			return err
		}
		rule := strings.TrimSpace(cmd.Rule)
		if rule == "" {
			return fmt.Errorf("rule is required")
		}
		g.AddRule(rule)
		return nil
	})

	b.OnCommand(func(cmd ResolveAskUser) error {
		if sctx.Approvals == nil {
			return fmt.Errorf("approvals not available")
		}
		return sctx.Approvals.ResolveAskUser(cmd.AskID, cmd.Answers)
	})

	// -------------------------------------------------------------------
	// Plan mode
	// -------------------------------------------------------------------

	b.OnCommand(func(cmd EnterPlanMode) error {
		if sctx.PlanMode == nil {
			return fmt.Errorf("plan mode not available")
		}
		// Enter() calls onChange → publishes PlanModeChanged.
		_, err := sctx.PlanMode.Enter()
		return err
	})

	b.OnCommand(func(cmd ExitPlanMode) error {
		if sctx.PlanMode == nil {
			return fmt.Errorf("plan mode not available")
		}
		if sctx.PlanMode.Mode() == planmode.ModeOff {
			return fmt.Errorf("not in plan mode")
		}
		// Exit() calls onChange → publishes PlanModeChanged.
		sctx.PlanMode.Exit()
		return nil
	})

	b.OnCommand(func(cmd StartPlanExecution) error {
		if sctx.PlanMode == nil {
			return fmt.Errorf("plan mode not available")
		}
		if cmd.CleanContext {
			if err := sctx.Agent.Reset(); err != nil {
				return fmt.Errorf("reset before execution: %w", err)
			}
		}
		// StartExecution() calls onChange → publishes PlanModeChanged.
		sctx.PlanMode.StartExecution()
		return nil
	})

	b.OnCommand(func(cmd StartPlanReview) error {
		if sctx.PlanMode == nil {
			return fmt.Errorf("plan mode not available")
		}
		// StartReview() calls onChange → publishes PlanModeChanged.
		sctx.PlanMode.StartReview()
		return nil
	})

	b.OnCommand(func(cmd ContinueRefining) error {
		if sctx.PlanMode == nil {
			return fmt.Errorf("plan mode not available")
		}
		// ContinueRefining() calls onChange → publishes PlanModeChanged.
		sctx.PlanMode.ContinueRefining()
		return nil
	})

	b.OnCommand(func(cmd FinishPlanReview) error {
		if sctx.PlanMode == nil {
			return fmt.Errorf("plan mode not available")
		}
		// ReviewDone() calls onChange → publishes PlanModeChanged.
		sctx.PlanMode.ReviewDone()
		return nil
	})

	// -------------------------------------------------------------------
	// Path policy
	// -------------------------------------------------------------------

	b.OnCommand(func(cmd SetPathScope) error {
		if sctx.PathPolicy == nil {
			return fmt.Errorf("path policy not available")
		}
		scope := strings.ToLower(cmd.Scope)
		// Normalize ws+N → workspace (extra paths come via AddAllowedPath).
		if strings.HasPrefix(scope, "ws") {
			scope = "workspace"
		}
		switch scope {
		case "workspace":
			sctx.PathPolicy.SetUnrestricted(false)
		case "unrestricted":
			sctx.PathPolicy.SetUnrestricted(true)
		default:
			return fmt.Errorf("invalid scope %q (options: workspace, unrestricted)", cmd.Scope)
		}
		sctx.Bus.Publish(ConfigChanged{
			SessionID: sctx.SessionID,
			PathScope: sctx.PathPolicy.Scope(),
		})
		return nil
	})

	b.OnCommand(func(cmd AddAllowedPath) error {
		if sctx.PathPolicy == nil {
			return fmt.Errorf("path policy not available")
		}
		if err := sctx.PathPolicy.AddPath(cmd.Path); err != nil {
			return err
		}
		sctx.Bus.Publish(ConfigChanged{
			SessionID: sctx.SessionID,
			PathScope: sctx.PathPolicy.Scope(),
		})
		return nil
	})

	b.OnCommand(func(cmd RemoveAllowedPath) error {
		if sctx.PathPolicy == nil {
			return fmt.Errorf("path policy not available")
		}
		if !sctx.PathPolicy.RemovePath(cmd.Path) {
			return fmt.Errorf("%s not in allowed paths", cmd.Path)
		}
		sctx.Bus.Publish(ConfigChanged{
			SessionID: sctx.SessionID,
			PathScope: sctx.PathPolicy.Scope(),
		})
		return nil
	})

	// -------------------------------------------------------------------
	// Additional queries
	// -------------------------------------------------------------------

	b.OnQuery(func(q GetSessionError) (string, error) {
		if sctx.State == nil {
			return "", nil
		}
		return sctx.State.LastError(), nil
	})

	b.OnQuery(func(q GetPendingApproval) (PendingApprovalInfo, error) {
		if sctx.Approvals == nil {
			return PendingApprovalInfo{}, nil
		}
		return sctx.Approvals.PendingInfo(), nil
	})

	// -------------------------------------------------------------------
	// Tree commands & queries
	// -------------------------------------------------------------------

	b.OnCommand(func(cmd BranchTo) error {
		if sctx.Tree == nil {
			return fmt.Errorf("branching not available (no session tree)")
		}
		if sctx.State != nil && sctx.State.Current() == StateRunning {
			return fmt.Errorf("cannot branch while agent is running")
		}
		if err := sctx.Tree.Branch(cmd.EntryID); err != nil {
			return err
		}
		// Rehydrate agent state from the new branch context
		msgs, epoch := sctx.Tree.BuildContext()
		if err := sctx.Agent.LoadState(msgs, epoch); err != nil {
			return fmt.Errorf("branch: load state: %w", err)
		}
		sctx.Bus.Publish(CommandExecuted{
			SessionID: sctx.SessionID,
			Command:   "branch",
			Messages:  msgs,
		})
		return nil
	})

	b.OnQuery(func(q GetDisplayMessages) ([]core.AgentMessage, error) {
		if sctx.Tree == nil || sctx.Tree.Len() == 0 {
			return sctx.Agent.Messages(), nil
		}
		return sctx.Tree.AllMessages(), nil
	})

	b.OnQuery(func(q GetBranchPoints) ([]BranchPoint, error) {
		if sctx.Tree == nil {
			return nil, nil
		}
		path := sctx.Tree.Path()
		currentIDs := make(map[string]bool, len(path))
		for _, e := range path {
			currentIDs[e.ID] = true
		}

		var points []BranchPoint
		for _, e := range sctx.Tree.Entries() {
			if e.Type != session.EntryMessage {
				continue
			}
			// Only user/assistant entries are valid branch targets
			if e.Message.Role != "user" && e.Message.Role != "assistant" {
				continue
			}
			label := firstLine(messageText(e.Message))
			children := sctx.Tree.Children(e.ID)
			points = append(points, BranchPoint{
				EntryID:       e.ID,
				Label:         label,
				Role:          e.Message.Role,
				Timestamp:     e.Timestamp.Unix(),
				BranchCount:   len(children),
				IsCurrentPath: currentIDs[e.ID],
			})
		}
		return points, nil
	})

	// -------------------------------------------------------------------
	// Plan submitted reactor — detects when submit_plan tool completes
	// -------------------------------------------------------------------

	b.Subscribe(func(e ToolExecEnded) {
		if e.ToolName == "submit_plan" && !e.IsError && !e.Rejected {
			if sctx.PlanMode != nil && sctx.PlanMode.OnPlanSubmitted() {
				rebuildSystemPrompt(sctx)
				sctx.Bus.Publish(PlanModeChanged{
					SessionID: sctx.SessionID,
					Mode:      string(planmode.ModeReady),
					PlanFile:  sctx.PlanMode.PlanFilePath(),
				})
			}
		}
	})

	// -------------------------------------------------------------------
	// ContextUpdated reactor — publishes context usage after state changes
	// -------------------------------------------------------------------

	publishContextUpdate := func() {
		model := sctx.Agent.Model()
		if model.MaxInput <= 0 {
			return
		}
		msgs := sctx.Agent.Messages()
		est := core.EstimateContextTokens(msgs, "", nil, sctx.Agent.CompactionEpoch())
		pct := (est.Tokens * 100) / model.MaxInput
		if pct > 100 {
			pct = 100
		}
		sctx.Bus.Publish(ContextUpdated{SessionID: sctx.SessionID, Percent: pct})
	}
	b.Subscribe(func(e RunEnded) { publishContextUpdate() })
	b.Subscribe(func(e CommandExecuted) { publishContextUpdate() })
	b.Subscribe(func(e ConfigChanged) { publishContextUpdate() })

	// --- Auto-verify ---
	// After a run that edited files, optionally run verify and re-send failures to agent.
	b.Subscribe(func(e RunEnded) {
		if !sctx.AutoVerify || sctx.CWD == "" {
			return
		}
		if e.Err != nil || !e.HadEdits {
			return
		}
		// Guardrail: max 2 auto-verify retries per user-initiated chain.
		count := autoVerifyCount.Add(1)
		if count > 2 {
			return
		}

		// Capture run generation so we can detect stale results.
		startRunGen := e.RunGen

		go func() {
			sctx.Bus.Publish(AutoVerifyStarted{SessionID: sctx.SessionID})

			ctx, cancel := context.WithTimeout(sctx.SessionCtx, 5*time.Minute)
			defer cancel()

			// Store cancel so new user runs can abort this verify.
			autoVerifyCancel.Store(&cancel)
			defer autoVerifyCancel.CompareAndSwap(&cancel, nil)

			result, err := verify.Execute(ctx, sctx.CWD)

			// Check if a newer run started while we were verifying.
			if sctx.RunGenAtomic.Load() != startRunGen {
				return // stale — discard results
			}

			if err != nil {
				sctx.Bus.Publish(AutoVerifyEnded{
					SessionID: sctx.SessionID, Err: err,
				})
				return
			}

			if result.AllPass {
				sctx.Bus.Publish(AutoVerifyEnded{
					SessionID: sctx.SessionID, AllPass: true,
				})
				autoVerifyCount.Store(0)
				return
			}

			summary := formatVerifyFailure(result)
			sctx.Bus.Publish(AutoVerifyEnded{
				SessionID: sctx.SessionID, Summary: summary,
			})

			// Re-send to agent if idle/error; drop if already running.
			if sctx.State != nil {
				current := sctx.State.Current()
				if current == StateIdle || current == StateError {
					_ = sctx.Bus.Execute(SendPrompt{
						Text:   summary,
						Custom: map[string]any{"source": "auto_verify"},
					})
				}
			}
		}()
	})
}

// startRun is the shared implementation for SendPrompt and SendPromptWithContent.
// It validates state, creates a per-run context, and spawns the agent run goroutine.
func startRun(sctx *SessionContext, label string, runFn func(ctx context.Context) ([]core.AgentMessage, error)) error {
	// State transition: idle/error → running.
	if sctx.State != nil {
		if err := sctx.State.Transition(StateRunning); err != nil {
			return fmt.Errorf("cannot send: %w", err)
		}
	}

	// Create per-run context with generation token.
	sctx.runMu.Lock()
	runCtx, gen := sctx.newRunContext()
	sctx.runMu.Unlock()

	// Notify subscribers of the run generation (single source of truth for runGen).
	sctx.Bus.Publish(RunStarted{SessionID: sctx.SessionID, RunGen: gen})

	// Capture message count before run to extract only new text.
	msgsBefore := len(sctx.Agent.Messages())

	go func() {
		// Open checkpoint.
		if sctx.Checkpoints != nil {
			cpLabel := label
			if len(cpLabel) > 60 {
				cpLabel = cpLabel[:60] + "…"
			}
			sctx.Checkpoints.Begin(cpLabel)
		}

		msgs, err := runFn(runCtx)

		// Close checkpoint: Discard on cancel, Commit otherwise.
		cancelled := runCtx.Err() != nil
		if sctx.Checkpoints != nil {
			if cancelled {
				sctx.Checkpoints.Discard()
			} else {
				sctx.Checkpoints.Commit()
			}
		}

		// Clear run cancel BEFORE state transition to prevent a race where
		// a new run starts (setting a new runCancel) and then this goroutine
		// clears it. The generation token ensures we only clear our own cancel.
		sctx.clearRunCancel(gen)

		// State transition.
		if sctx.State != nil {
			if err != nil && !cancelled {
				_ = sctx.State.TransitionWithError(StateError, err.Error())
			} else {
				_ = sctx.State.Transition(StateIdle)
			}
		}

		// Extract final text and detect edits from NEW messages.
		var finalText string
		var hadEdits bool
		if len(msgs) > msgsBefore {
			newMsgs := msgs[msgsBefore:]
			finalText = extractFinalAssistantText(newMsgs)
			hadEdits = hasSuccessfulEdits(newMsgs)
		}

		// Publish run result.
		var runErr error
		if err != nil && !cancelled {
			runErr = err
		}
		sctx.Bus.Publish(RunEnded{
			SessionID: sctx.SessionID,
			RunGen:    gen,
			FinalText: finalText,
			Err:       runErr,
			HadEdits:  hadEdits,
		})
	}()
	return nil
}

// hasSuccessfulEdits checks tool_result messages for successful file-editing tools.
func hasSuccessfulEdits(msgs []core.AgentMessage) bool {
	editTools := map[string]bool{
		"edit":        true,
		"write":       true,
		"multiedit":   true,
		"apply_patch": true,
	}
	for _, msg := range msgs {
		if msg.Role != "tool_result" {
			continue
		}
		if editTools[msg.ToolName] && !msg.IsError {
			return true
		}
	}
	return false
}

// firstLine returns the first line of text, truncated to 80 chars.
func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

// messageText extracts the concatenated text content from an AgentMessage.
func messageText(msg core.AgentMessage) string {
	var parts []string
	for _, c := range msg.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "")
}

// formatVerifyFailure builds a markdown summary of failing verify checks.
func formatVerifyFailure(result verify.Result) string {
	var sb strings.Builder
	sb.WriteString("Auto-verify failed. Fix the following issues:\n\n")
	for _, ch := range result.Checks {
		if !ch.Passed {
			output := ch.Output
			if len(output) > 2000 {
				output = output[:2000] + "\n...(truncated)"
			}
			fmt.Fprintf(&sb, "**%s** (exit %d):\n```\n%s\n```\n\n", ch.Name, ch.ExitCode, output)
		}
	}
	return sb.String()
}
