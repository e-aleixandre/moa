package bus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/goal"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/planmode"
	"github.com/ealeixandre/moa/pkg/session"
	"github.com/ealeixandre/moa/pkg/tasks"
	"github.com/ealeixandre/moa/pkg/verify"
)

// rebuildSystemPrompt recomposes the agent's system prompt from the base prompt
// plus any active mode fragments (plan mode, goal mode). Called after any mode
// transition. Plan mode and goal mode are independent — a session may have
// either, both, or neither. No-op if neither mode is present.
func rebuildSystemPrompt(sctx *SessionContext) {
	if sctx.PlanMode == nil && sctx.Goal == nil {
		return
	}
	prompt := sctx.BaseSystemPrompt
	if sctx.PlanMode != nil {
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
	}
	if sctx.Goal != nil && sctx.Goal.Active() {
		prompt += "\n\n" + goal.GoalDirective(sctx.Goal.Info())
	}
	_ = sctx.Agent.SetSystemPrompt(prompt)
}

// RegisterHandlers registers command and query handlers for a session on its bus.
// Call once after creating a SessionContext.
func RegisterHandlers(sctx *SessionContext) {
	b := sctx.Bus

	// Serializes manual /verify runs (bus command and queued barrier share it).
	var manualVerifyRunning atomic.Bool

	// Background jobs can continue after the foreground agent reaches idle.
	// Keep their lifecycle in the runtime so headless callers can wait for the
	// same complete result chain that interactive frontends observe.
	b.Subscribe(func(e SubagentStarted) { sctx.trackBackgroundEvent(e) })
	b.Subscribe(func(e SubagentEnded) { sctx.trackBackgroundEvent(e) })
	b.Subscribe(func(e BashJobStarted) { sctx.trackBackgroundEvent(e) })
	b.Subscribe(func(e BashJobSettled) { sctx.trackBackgroundEvent(e) })

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
		// Centralize the ID invariant: every queued steer has a stable ID even
		// if a caller (CLI, internal) forgot to mint one.
		if cmd.ID == "" {
			cmd.ID = core.NewSteerID()
		}
		if !sctx.Agent.Steer(core.SteerItem{ID: cmd.ID, Text: cmd.Text, Content: cmd.Content, Internal: cmd.Internal}) {
			return ErrSteerQueueFull
		}
		// Kick the pump after enqueuing. While a run is in flight this is a
		// no-op (the running agent drains the steer at its next turn boundary);
		// but if the session is idle — e.g. the serve layer observed a
		// non-empty queue and steered, then the pump drained it to idle in
		// between — this delivers the otherwise-orphaned steer as a new run.
		requestPump(sctx)
		return nil
	})

	b.OnCommand(func(cmd QueueCommand) error {
		if cmd.ID == "" {
			cmd.ID = core.NewSteerID()
		}
		// A barrier carries the raw command line in both Command (the executable
		// form) and Text (display). It is never injected as a message.
		if !sctx.Agent.Steer(core.SteerItem{ID: cmd.ID, Text: cmd.Raw, Command: cmd.Raw}) {
			return ErrSteerQueueFull
		}
		sctx.Bus.Publish(CommandQueued{SessionID: sctx.SessionID, ID: cmd.ID, Raw: cmd.Raw})
		// Kick the pump after enqueuing (same orphan-race close as SteerAgent):
		// if the session was busy when the caller classified it but the run's
		// RunEnded drained an empty queue before this barrier landed, the pump
		// would never revisit it. A no-op while a run is in flight; at idle it
		// executes the barrier at once.
		requestPump(sctx)
		return nil
	})

	b.OnCommand(func(cmd RunManualVerify) error {
		if err := RequireManualVerifyAllowed(sctx.Bus); err != nil {
			return err
		}
		// Occupy the session state for the whole verify (idle → running → idle),
		// like a run: this is what keeps a queued /verify barrier in its position
		// (a concurrent SendPrompt can't slip in while it runs). A second caller
		// fails the transition and gets ErrSessionBusy. The atomic below is a
		// belt against two verifies that both somehow saw idle. (The serve/TUI
		// direct /verify commands are routed through here in a later commit; for
		// now only the queued barrier uses this state-occupying path.)
		if sctx.State != nil {
			if err := sctx.State.Transition(StateRunning); err != nil {
				return ErrSessionBusy
			}
		}
		settle := func() {
			if sctx.State != nil {
				_ = sctx.State.Transition(StateIdle)
			}
			// No RunEnded fires for a verify (it isn't a run), so kick the pump
			// ourselves so a following queued item drains.
			requestPump(sctx)
		}
		if !manualVerifyRunning.CompareAndSwap(false, true) {
			settle()
			return ErrVerifyRunning
		}
		defer func() {
			manualVerifyRunning.Store(false)
			settle()
		}()

		sctx.Bus.Publish(AutoVerifyStarted{SessionID: sctx.SessionID})
		ctx, cancel := context.WithTimeout(sctx.SessionCtx, 5*time.Minute)
		defer cancel()

		result, err := verify.Execute(ctx, sctx.CWD)
		if err != nil {
			sctx.Bus.Publish(AutoVerifyEnded{SessionID: sctx.SessionID, Err: err})
			return err
		}
		if result.AllPass {
			sctx.Bus.Publish(AutoVerifyEnded{SessionID: sctx.SessionID, AllPass: true})
			return nil
		}
		summary := formatVerifyFailure(result)
		sctx.Bus.Publish(AutoVerifyEnded{SessionID: sctx.SessionID, Summary: summary})
		return fmt.Errorf("%s", summary)
	})

	b.OnCommand(func(cmd CancelSteer) error {
		discarded := sctx.Agent.CancelSteer()
		// Broadcast the invalidation so every client of this session clears its
		// queued chips (the queue is shared/authoritative).
		sctx.Bus.Publish(SteersCanceled{
			SessionID:     sctx.SessionID,
			AttachmentIDs: steerAttachmentIDs(discarded),
		})
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
			Provider:  newModel.Provider,
			Thinking:  sctx.Agent.ThinkingLevel(),
		})
		return nil
	})

	b.OnCommand(func(cmd SetThinking) error {
		if !core.IsValidThinkingLevel(cmd.Level) {
			return fmt.Errorf("invalid thinking level %q (options: %s)", cmd.Level, core.ThinkingLevelOptions())
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
		sctx.resetSessionCost()
		if sctx.SessionCheckpoint != nil {
			sctx.SessionCheckpoint.Clear()
		}
		sctx.Bus.Publish(CommandExecuted{
			SessionID: sctx.SessionID,
			Command:   "clear",
		})
		return nil
	})

	b.OnCommand(func(cmd CompactSession) error {
		// A manual compact occupies the agent's run slot for seconds, so it
		// must occupy the session too: transition to running so frontends
		// switch the input to queue mode (steer) and Manager.Send/requireIdle
		// treat the session as busy instead of racing a concurrent run.
		if sctx.State != nil {
			if err := sctx.State.Transition(StateRunning); err != nil {
				return fmt.Errorf("cannot compact: %w", err)
			}
		}
		// Emit CompactionStarted/Ended explicitly (agent.Compact doesn't emit lifecycle events).
		// Set the authoritative flag BEFORE publishing so a concurrent reconnect
		// snapshot cut observes compacting=true consistently with the streamed
		// events; the defer is a safety net against a panic path.
		sctx.setCompacting(true)
		defer sctx.setCompacting(false)
		sctx.Bus.Publish(CompactionStarted{SessionID: sctx.SessionID})
		result, err := func() (p *core.CompactionPayload, e error) {
			// Recover panics so the state machine can never be left stuck in
			// running by a crashing Compact.
			defer func() {
				if r := recover(); r != nil {
					e = fmt.Errorf("compaction panic: %v", r)
				}
			}()
			return sctx.Agent.Compact(sctx.SessionCtx)
		}()
		// Settle the state BEFORE publishing results, mirroring startRun:
		// reactors observing CompactionEnded must see idle/error, not running.
		if sctx.State != nil {
			if err != nil {
				_ = sctx.State.TransitionWithError(StateError, err.Error())
			} else {
				_ = sctx.State.Transition(StateIdle)
			}
		}
		if err != nil {
			sctx.setCompacting(false)
			sctx.Bus.Publish(CompactionEnded{SessionID: sctx.SessionID, Err: err})
			// A message queued during the failed compact must still be
			// delivered (Error→Running is a valid transition).
			requestPump(sctx)
			return err
		}
		// Signal compaction ended (with or without payload).
		sctx.setCompacting(false)
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
		// Messages sent while the compact held the session busy were queued as
		// steers, but no run is coming to drain them — pump the queue now.
		requestPump(sctx)
		return nil
	})

	b.OnCommand(func(cmd PrepareCompactSession) error {
		if err := reserveRunSlot(sctx); err != nil {
			return fmt.Errorf("cannot prepare compact: %w", err)
		}
		// A barrier prevents both existing and newly accepted steers from being
		// consumed by the ephemeral preparation run. If queue pump owns this
		// command, its barrier already provides the same boundary.
		barrierID := "prepare-compact-internal-" + core.NewSteerID()
		head, hasHead := sctx.Agent.PeekQueueHead()
		addedBarrier := !hasHead || !head.IsBarrier()
		if addedBarrier {
			sctx.Agent.PushSteersFront([]core.SteerItem{{ID: barrierID, Command: barrierID}})
		}
		const prompt = "Prepare this conversation for imminent compaction. Do not continue the user's task. Only update existing relevant tracking or docs; never create docs merely for compaction. Use the ephemeral checkpoint for active non-reconstructible data, never memory. You may do nothing. Briefly report what you prepared."
		launchRun(sctx, "prepare compact", func(ctx context.Context) ([]core.AgentMessage, error) {
			defer func() {
				if addedBarrier {
					sctx.Agent.PopQueueBarrier(barrierID)
				}
			}()
			before, epoch, err := snapshotConversation(sctx)
			if err != nil {
				return nil, err
			}
			restored := false
			defer func() {
				if !restored {
					_ = restoreConversation(sctx, before, epoch)
				}
			}()
			if _, err = sendPrepareCompact(ctx, sctx, prompt); err != nil {
				return nil, err
			}
			if err = restoreConversation(sctx, before, epoch); err != nil {
				return nil, err
			}
			restored = true
			sctx.setCompacting(true)
			defer sctx.setCompacting(false)
			sctx.Bus.Publish(CompactionStarted{SessionID: sctx.SessionID})
			text, gen := "", uint64(0)
			if sctx.SessionCheckpoint != nil {
				text, gen = sctx.SessionCheckpoint.Read()
			}
			payload, err := compactWithCheckpoint(ctx, sctx, text)
			if err != nil {
				sctx.Bus.Publish(CompactionEnded{SessionID: sctx.SessionID, Err: err})
				return nil, err
			}
			if payload == nil {
				sctx.Bus.Publish(CompactionEnded{SessionID: sctx.SessionID})
				sctx.Bus.Publish(CommandExecuted{SessionID: sctx.SessionID, Command: "prepare-compact-noop", Messages: sctx.Agent.Messages()})
				return sctx.Agent.Messages(), nil
			}
			sctx.Bus.Publish(CompactionEnded{SessionID: sctx.SessionID, Payload: payload})
			sctx.Bus.Drain(2 * time.Second)
			if sctx.PersistNow != nil {
				if err := sctx.PersistNow(); err != nil {
					return nil, err
				}
			}
			if sctx.SessionCheckpoint != nil {
				if err := clearPersistedCheckpoint(sctx.SessionCheckpoint, text, gen, sctx.PersistNow); err != nil {
					return nil, err
				}
			}
			sctx.Bus.Publish(CommandExecuted{SessionID: sctx.SessionID, Command: "prepare-compact", Messages: sctx.Agent.Messages()})
			return sctx.Agent.Messages(), nil
		})
		return nil
	})

	b.OnCommand(func(cmd UndoLastChange) error {
		if sctx.Checkpoints == nil {
			return fmt.Errorf("checkpoints not available")
		}
		return sctx.Checkpoints.UndoAndRestore()
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

	b.OnQuery(func(q GetSessionCost) (float64, error) {
		return sctx.sessionCostTotal(), nil
	})

	b.OnQuery(func(q GetRunTokens) (RunTokens, error) {
		sctx.runTokenMu.Lock()
		defer sctx.runTokenMu.Unlock()
		return RunTokens{Up: sctx.runTokensUp, Down: sctx.runTokensDown}, nil
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

	b.OnQuery(func(q GetGoal) (GoalInfo, error) {
		if sctx.Goal == nil {
			return GoalInfo{}, nil
		}
		info := sctx.Goal.Info()
		return GoalInfo{
			Active:        info.Active,
			Objective:     info.Objective,
			WorkDir:       info.WorkDir,
			Iteration:     info.Iteration,
			Stalled:       info.Stalled,
			MaxIterations: info.MaxIterations,
			MaxStalled:    info.MaxStalled,
			Verifying:     sctx.GoalVerifying(),
		}, nil
	})

	b.OnQuery(func(q GetCompactionEpoch) (int, error) {
		return sctx.Agent.CompactionEpoch(), nil
	})

	b.OnQuery(func(q GetCompacting) (bool, error) {
		return sctx.Compacting(), nil
	})

	b.OnQuery(func(q GetPendingSteers) ([]core.SteerItem, error) {
		return sctx.Agent.PendingSteers(), nil
	})

	b.OnQuery(func(q GetQueueLen) (int, error) {
		return sctx.Agent.QueueLen(), nil
	})

	b.OnQuery(func(q GetUndeliveredNativeBytes) (int64, error) {
		return sctx.Agent.NativeDocBytesUndelivered(), nil
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
		// Strict-order gate (INV-2): a genuine user prompt must not start a run
		// while the queue rail holds pending items — it would jump ahead of a
		// queued barrier/steer. Convert it into a steer at the tail of the queue
		// instead, preserving send order; the pump delivers it in turn. Internal
		// producers (the goal loop, auto-verify) are exempt: they are the
		// machinery the queue is waiting on, not new user turns, and steering
		// them would strip their Custom source.
		if !isInternalPromptSource(cmd.Custom) && sctx.Agent.QueueLen() > 0 {
			id := cmd.MsgID
			if id == "" {
				id = core.NewSteerID()
			}
			if !sctx.Agent.Steer(core.SteerItem{ID: id, Text: cmd.Text}) {
				return ErrSteerQueueFull
			}
			// Always kick the pump after enqueuing: this closes the orphan-steer
			// race where the pump drained the queue empty between our QueueLen
			// read and this Steer. A coalesced pump pass guarantees our steer is
			// delivered (immediately if idle, or on the current run's RunEnded).
			requestPump(sctx)
			return nil
		}
		// Reset auto-verify counter on user-initiated prompts.
		if cmd.Custom == nil || cmd.Custom["source"] != "auto_verify" {
			autoVerifyCount.Store(0)
			cancelAutoVerify()
		}
		// A genuine user prompt (not the goal loop's own relaunch) aborts any
		// in-flight goal verification so stale build/tests don't run against the
		// new run's edits.
		if cmd.Custom == nil || cmd.Custom["source"] != "goal" {
			if sctx.cancelGoalVerify != nil {
				sctx.cancelGoalVerify()
			}
		}
		return startRun(sctx, cmd.Text, func(ctx context.Context) ([]core.AgentMessage, error) {
			if cmd.Custom != nil {
				return sctx.Agent.SendWithCustom(ctx, cmd.Text, cmd.Custom)
			}
			if cmd.MsgID != "" {
				return sctx.Agent.SendWithMsgID(ctx, cmd.Text, cmd.MsgID)
			}
			return sctx.Agent.Send(ctx, cmd.Text)
		})
	})

	b.OnCommand(func(cmd SendPromptWithContent) error {
		// Strict-order gate (INV-2): queue behind pending items instead of
		// jumping ahead. Content sends are always user-initiated, so no source
		// exemption applies.
		if sctx.Agent.QueueLen() > 0 {
			if !sctx.Agent.Steer(core.SteerItem{ID: core.NewSteerID(), Content: cmd.Content}) {
				return ErrSteerQueueFull
			}
			requestPump(sctx) // close the orphan-steer race (see SendPrompt)
			return nil
		}
		// User-initiated content send resets auto-verify counter.
		autoVerifyCount.Store(0)
		cancelAutoVerify()
		// Also abort any in-flight goal verification (stale build/tests).
		if sctx.cancelGoalVerify != nil {
			sctx.cancelGoalVerify()
		}
		label := "content"
		if len(cmd.Content) > 0 && cmd.Content[0].Text != "" {
			label = cmd.Content[0].Text
		}
		// Reserve this send's native bytes in the inflight ledger BEFORE the run
		// goroutine starts: SendWithContent appends to history asynchronously, so
		// without the reservation a concurrent send (steering, since we just
		// reserved the run slot) could read the quota before these bytes are
		// countable in history and admit content past the per-session cap.
		// SendWithContent settles it once the message lands (or releases it if
		// the send never runs); if startRun itself fails, release it here.
		nativeBytes := core.NativeDocBytes(cmd.Content)
		sctx.Agent.ReserveNativeDocBytes(nativeBytes)
		if err := startRun(sctx, label, func(ctx context.Context) ([]core.AgentMessage, error) {
			return sctx.Agent.SendWithContent(ctx, cmd.Content)
		}); err != nil {
			sctx.Agent.ReleaseNativeDocBytes(nativeBytes)
			return err
		}
		return nil
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
			// Keep the gate and approval bridge alive. ModeYolo approves ordinary
			// calls, while Gate.Check still routes hard-coded dangerous commands
			// through an explicit approval.
			if sctx.GetGate() == nil {
				g := permission.New(newMode, sctx.GateConfig)
				sctx.SetGate(g)
				if sctx.Approvals != nil {
					sctx.Approvals.StartPermissionBridge(sctx.SessionCtx, g)
				}
			} else {
				sctx.GetGate().SetMode(newMode)
			}
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

		modeStr := string(sctx.GetGate().Mode())
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
		if err := sctx.Approvals.ResolvePermission(cmd.PermissionID, cmd.Approved, cmd.Feedback, cmd.AllowPattern); err != nil {
			return err
		}
		// Persist "always allow" patterns to project config so they survive a
		// restart. The Gate already applied the pattern in memory; this is
		// best-effort — a save failure must not fail the resolution.
		if pattern := strings.TrimSpace(cmd.AllowPattern); pattern != "" && sctx.CWD != "" {
			if err := core.SaveProjectConfig(sctx.CWD, func(c *core.MoaConfig) {
				if !slices.Contains(c.Permissions.Allow, pattern) {
					c.Permissions.Allow = append(c.Permissions.Allow, pattern)
				}
			}); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to persist allow pattern %q: %v\n", pattern, err)
			}
		}
		return nil
	})

	b.OnCommand(func(cmd ResolvePermissionExact) error {
		if sctx.Approvals == nil {
			return fmt.Errorf("approvals not available")
		}
		return sctx.Approvals.ResolvePermissionExact(cmd.Snapshot, cmd.Approved)
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
			// Reset no longer drops the queue (it preserves it for the queued
			// /clear reset-in-place case); a clean-context plan execution is a
			// genuine clean slate, so drop any queued steers explicitly.
			discarded := sctx.Agent.CancelSteer()
			sctx.Bus.Publish(SteersCanceled{
				SessionID:     sctx.SessionID,
				AttachmentIDs: steerAttachmentIDs(discarded),
			})
			// Agent.Reset alone leaves the persisted tree and the syncer's old
			// baseline behind. Replace both in the same transition so the next
			// execution turn cannot revive or splice into the planning history.
			sctx.Tree = session.NewTree()
			if sctx.treeSyncer != nil {
				sctx.treeSyncer.Reset(sctx.Tree, 0)
			}
			sctx.resetSessionCost()
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
	// Goal mode
	// -------------------------------------------------------------------

	b.OnCommand(func(cmd EnterGoal) error {
		if sctx.Goal == nil {
			return fmt.Errorf("goal mode not available")
		}
		if sctx.Goal.Active() {
			return fmt.Errorf("already in goal mode")
		}
		if strings.TrimSpace(cmd.Objective) == "" {
			return fmt.Errorf("goal objective is required")
		}
		if sctx.State != nil && sctx.State.Current() == StateRunning {
			return fmt.Errorf("cannot start a goal while the agent is running")
		}
		workDir, err := resolveGoalWorkDir(sctx, cmd.WorkDir)
		if err != nil {
			return err
		}
		statePath := cmd.StatePath
		if statePath == "" {
			statePath = goal.DefaultStatePath
		}
		if !filepath.IsAbs(statePath) {
			statePath = filepath.Join(workDir, statePath)
		}
		// Lower the compaction threshold for the duration of the goal, remembering
		// the previous value so we can restore it (not blindly reset to 0) on exit.
		sctx.goalPrevCompactAt = sctx.Agent.CompactAt()
		if cmd.CompactAt > 0 {
			if err := sctx.Agent.SetCompactAt(cmd.CompactAt); err != nil {
				return err
			}
		}
		// Interpret the configured per-run MaxBudget as the goal's TOTAL budget:
		// the driver caps each iteration at the remaining pool so the loop's
		// cumulative spend can't exceed it (an unbounded N×budget otherwise).
		// An explicit --budget overrides this.
		sctx.goalPrevMaxBudget = sctx.Agent.MaxBudget()
		totalBudget := cmd.TotalBudget
		if totalBudget <= 0 {
			totalBudget = sctx.goalPrevMaxBudget
		}
		// Apply an explicit --budget up front so it also binds the FIRST run (the
		// driver only caps subsequent iterations). Hard-fail if it can't bind
		// (e.g. the model has no pricing) rather than silently promising a ceiling
		// we won't enforce. The derived-from-MaxBudget case already holds on the
		// first run, so it stays best-effort below.
		if cmd.TotalBudget > 0 {
			if err := sctx.Agent.SetMaxBudget(cmd.TotalBudget); err != nil {
				if cmd.CompactAt > 0 {
					_ = sctx.Agent.SetCompactAt(sctx.goalPrevCompactAt) // roll back the compaction change
				}
				return fmt.Errorf("goal: cannot apply --budget: %w", err)
			}
		}
		// Enter() creates STATE.md and fires onChange → rebuilds the system
		// prompt (injecting the directive) and publishes GoalChanged.
		if err := sctx.Goal.Enter(goal.Options{
			Objective:     cmd.Objective,
			StatePath:     statePath,
			WorkDir:       workDir,
			VerifierSpec:  cmd.VerifierSpec,
			MaxIterations: cmd.MaxIterations,
			MaxStalled:    cmd.MaxStalled,
			Timeout:       cmd.Timeout,
			TotalBudget:   totalBudget,
			VerifyTimeout: cmd.VerifyTimeout,
			VerifyOneShot: cmd.VerifyOneShot,
		}); err != nil {
			if cmd.CompactAt > 0 {
				_ = sctx.Agent.SetCompactAt(sctx.goalPrevCompactAt) // roll back on failure
			}
			if cmd.TotalBudget > 0 {
				_ = sctx.Agent.SetMaxBudget(sctx.goalPrevMaxBudget) // roll back the budget too
			}
			return err
		}
		// Baseline the commit so the driver's progress check has a reference for
		// the first iteration (progress = new edits or a new commit).
		if workDir != "" {
			sctx.Goal.SetLastCommit(runGit(sctx.SessionCtx, workDir, "rev-parse", "HEAD"))
		}
		// Persistent start marker in the conversation (survives reload). Appended
		// while the agent is still idle, before the first kick starts a run.
		appendGoalMarker(sctx, "🎯 Goal started: "+cmd.Objective, map[string]any{
			"phase":     "start",
			"objective": cmd.Objective,
		})
		// Kick the first iteration. The driver takes over from RunEnded on.
		return sctx.Bus.Execute(SendPrompt{
			SessionID: sctx.SessionID,
			Text:      goalFirstKick(sctx.Goal.Info()),
			Custom:    map[string]any{"source": "goal"},
		})
	})

	b.OnCommand(func(cmd ExitGoal) error {
		if sctx.Goal == nil {
			return fmt.Errorf("goal mode not available")
		}
		if !sctx.Goal.Active() {
			return fmt.Errorf("not in goal mode")
		}
		stopGoal(sctx, "stopped by user")
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

	b.OnQuery(func(q GetPermissionDecisionSnapshot) (PermissionDecisionSnapshot, error) {
		if sctx.Approvals == nil {
			return PermissionDecisionSnapshot{}, ErrPermissionDecisionSnapshotUnavailable
		}
		return sctx.Approvals.PendingPermissionDecisionSnapshot()
	})

	// -------------------------------------------------------------------
	// Tree commands & queries
	// -------------------------------------------------------------------

	b.OnCommand(func(cmd BranchTo) error {
		if sctx.Tree == nil {
			return fmt.Errorf("branching not available (no session tree)")
		}
		// Branching mutates the tree's leaf and then rehydrates the agent via
		// LoadState, which fails while a run is in flight (StateRunning) or a
		// permission is pending (StatePermission) — both keep the agent's run
		// cancel set. Reject any non-terminal state up front so we never move
		// the leaf to a branch the agent can't actually adopt.
		if sctx.State != nil {
			if s := sctx.State.Current(); s != StateIdle && s != StateError {
				return fmt.Errorf("cannot branch while agent is busy (%s)", s)
			}
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
		// Prefer the syncer: it composes the tree history with the in-flight
		// turn (agent messages not yet synced), so a mid-run snapshot is
		// complete. Falls back to tree/agent when no syncer is registered.
		if sctx.treeSyncer != nil {
			return sctx.treeSyncer.DisplayMessages(), nil
		}
		if sctx.Tree != nil {
			if msgs := sctx.Tree.AllMessages(); len(msgs) > 0 {
				return msgs, nil
			}
		}
		return sctx.Agent.Messages(), nil
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
			// Skip targets that would leave a dangling tool_call (e.g. an
			// assistant turn whose tool results haven't landed on this path
			// yet). Branch() enforces the same rule; filtering here keeps
			// the picker from ever offering a target it would reject.
			if err := sctx.Tree.ValidBranchTarget(e.ID); err != nil {
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

	// Run-token reactor — derive authoritative logical traffic from the main
	// agent's own history, scoped by the run-start baseline. This avoids
	// provider usage, resent context, and subagent traffic.
	recomputeRunTokens := func(runGen uint64) {
		sctx.runTokenMu.Lock()
		baseline := sctx.runTokenBaseline
		if runGen != sctx.runTokensGen {
			sctx.runTokenMu.Unlock()
			return
		}
		sctx.runTokenMu.Unlock()

		msgs := sctx.Agent.Messages()
		if baseline > len(msgs) {
			baseline = len(msgs)
		}
		up, down := 0, 0
		for _, m := range msgs[baseline:] {
			switch m.Role {
			case "user", "tool_result":
				up += core.EstimateTokens(m.Message)
			case "assistant":
				down += core.EstimateOutputTokens(m.Message)
			}
		}

		sctx.runTokenMu.Lock()
		if runGen != sctx.runTokensGen {
			sctx.runTokenMu.Unlock()
			return
		}
		sctx.runTokenBaseline = baseline
		sctx.runTokensUp = up
		sctx.runTokensDown = down
		sctx.runTokenMu.Unlock()
		sctx.Bus.Publish(RunTokensUpdated{SessionID: sctx.SessionID, RunGen: runGen, Up: up, Down: down})
	}
	// One all-event subscriber preserves publication order across RunStarted,
	// MessageEnded, and ToolExecEnded. Separate typed subscribers could race a
	// fast message completion ahead of its run baseline.
	b.SubscribeAll(func(event any) {
		switch e := event.(type) {
		case RunStarted:
			resetRunTokens(sctx, e.RunGen)
		case MessageEnded:
			recomputeRunTokens(e.RunGen)
		case ToolExecEnded:
			recomputeRunTokens(e.RunGen)
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

	// Queue pump: at every idle point, drain the unified queue rail — execute
	// queued barrier commands and start runs for trailing steers. RunEnded is
	// the normal idle signal; the manual compact/verify paths call requestPump
	// directly (they hold the session busy without a run). requestPump coalesces
	// the signals (and a barrier that itself ends a run) into one non-overlapping
	// pass. On a user abort the agent clears its own steer buffer as the
	// cancelled run ends, so an aborted run's RunEnded simply finds an empty
	// queue here — nothing to guard against.
	b.Subscribe(func(e RunEnded) { requestPump(sctx) })

	// When goal mode ends, drain any barriers/steers that were enqueued while
	// the pump was abstaining (it yields the idle slot to the goal driver). The
	// goal's final RunEnded is not a reliable trigger: its async subscribers may
	// see Goal.Active()==true when the pump reactor runs, so it could abstain
	// and then no further idle signal would arrive. GoalEnded fires after the
	// driver clears goal state, closing that gap.
	b.Subscribe(func(e GoalEnded) { requestPump(sctx) })

	// -------------------------------------------------------------------
	// SessionCostUpdated reactor — accumulates the session's USD spend from
	// the main run (RunEnded.Cost) and each subagent (SubagentEnded.CostUSD),
	// so TUI and web report the same figure from one source of truth.
	// -------------------------------------------------------------------
	b.Subscribe(func(e RunEnded) {
		if e.Cost == 0 {
			return
		}
		total := sctx.addSessionCost(e.Cost)
		sctx.Bus.Publish(SessionCostUpdated{SessionID: sctx.SessionID, TotalUSD: total, RunUSD: e.Cost})
	})
	b.Subscribe(func(e SubagentEnded) {
		if e.CostUSD == 0 {
			return
		}
		total := sctx.addSessionCost(e.CostUSD)
		sctx.Bus.Publish(SessionCostUpdated{SessionID: sctx.SessionID, TotalUSD: total, RunUSD: e.CostUSD})
	})
	b.Subscribe(func(e CompactionEnded) {
		// Automatic compactions are bridged from the running agent and their
		// usage is already folded into RunEnded.Cost.
		if e.CostIncludedInRun || sctx.Agent == nil || e.Payload == nil || e.Payload.Usage == nil {
			return
		}
		pricing := sctx.Agent.Model().Pricing
		if pricing == nil {
			return
		}
		cost := pricing.Cost(*e.Payload.Usage)
		if cost <= 0 {
			return
		}
		total := sctx.addSessionCost(cost)
		sctx.Bus.Publish(SessionCostUpdated{SessionID: sctx.SessionID, TotalUSD: total, RunUSD: cost})
	})

	// Clear approvals orphaned by an aborted run so no stale modal lingers.
	// Pass the ended run's generation so a newer run's live approval (from an
	// immediately re-sent prompt) is spared.
	b.Subscribe(func(e RunEnded) {
		if sctx.Approvals != nil {
			sctx.Approvals.ClearPending(e.RunGen)
		}
	})

	// --- Auto-verify ---
	// After a run that edited files, optionally run verify and re-send failures to agent.
	b.Subscribe(func(e RunEnded) {
		if !sctx.AutoVerify || sctx.CWD == "" {
			return
		}
		// Goal mode owns the run→verify→relaunch loop; stand down so the two
		// reactors don't both re-send prompts on the same RunEnded.
		if sctx.Goal != nil && sctx.Goal.Active() {
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
		sctx.beginAutoVerify()

		go func() {
			defer sctx.endAutoVerify()
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

	// --- Goal driver ---
	// When the maker stops in goal mode, a cheap separate verifier judges the
	// objective and the loop either ends (finite success or a backstop) or
	// relaunches the maker with feedback. Modeled on the auto-verify reactor.
	var goalVerifyCancel atomic.Pointer[context.CancelFunc]
	// cancelGoalVerify aborts an in-flight goal verification (build/tests + the
	// verifier LLM call). Called when the user starts a new run or stops the
	// goal, so stale checks don't run concurrently with fresh edits.
	cancelGoalVerify := func() {
		if fn := goalVerifyCancel.Swap(nil); fn != nil {
			(*fn)()
		}
	}
	sctx.cancelGoalVerify = cancelGoalVerify
	b.Subscribe(func(e RunEnded) {
		if sctx.Goal == nil || !sctx.Goal.Active() {
			return
		}

		// Accumulate this run's cost and enforce the cumulative-budget ceiling
		// first: a budget-exhausted run aborts with e.Err set, so this must run
		// before the error early-return below (else the loop would just pause with
		// the budget already blown).
		spent := sctx.Goal.AddSpent(e.Cost)
		info := sctx.Goal.Info()
		if info.TotalBudget > 0 && spent >= info.TotalBudget {
			stopGoal(sctx, fmt.Sprintf("reached budget ($%.2f of $%.2f)", spent, info.TotalBudget))
			return
		}

		// An errored/aborted run doesn't consume an iteration — leave the loop
		// paused so a user can inspect and resume.
		if e.Err != nil {
			return
		}

		startRunGen := e.RunGen

		// Backstops that don't depend on the verdict — checked before spending
		// a verifier call.
		it := sctx.Goal.BeginIteration()
		if !info.Deadline.IsZero() && time.Now().After(info.Deadline) {
			stopGoal(sctx, "reached time limit")
			return
		}

		// Separate budgets: building the evidence runs the project's real checks
		// (build + full test suite via verify.Execute), which can take minutes.
		// Sharing a single 2-min context with the verifier starved the verifier's
		// own timeout and produced systematic "context deadline exceeded" errors.
		// Give the evidence a generous budget and the verifier a fresh context
		// derived from the session (not the already-spent evidence context).
		//
		// The contexts and cancel handle are created and registered here —
		// synchronously, before the goroutine starts — so a user prompt arriving
		// in the gap can't miss the cancel and let a stale build/tests run against
		// fresh edits.
		evidenceCtx, evidenceCancel := context.WithTimeout(sctx.SessionCtx, 10*time.Minute)
		verifyCtx, verifyCancel := context.WithCancel(sctx.SessionCtx)
		var combined context.CancelFunc = func() {
			evidenceCancel()
			verifyCancel()
		}
		goalVerifyCancel.Store(&combined)
		sctx.beginGoalVerify()
		sctx.Bus.Publish(GoalVerifyStarted{SessionID: sctx.SessionID, Iteration: it})

		go func() {
			defer func() {
				sctx.endGoalVerify()
				sctx.Bus.Publish(GoalVerifyEnded{SessionID: sctx.SessionID, Iteration: it, Verifying: sctx.GoalVerifying()})
				goalVerifyCancel.CompareAndSwap(&combined, nil)
				evidenceCancel()
				verifyCancel()
			}()

			// A user prompt may have cancelled us before the goroutine got
			// scheduled — bail before spending minutes on build/tests.
			if evidenceCtx.Err() != nil || sctx.RunGenAtomic.Load() != startRunGen {
				return
			}

			evidence, checkGate := buildGoalEvidence(evidenceCtx, goalWorkDir(sctx, info), e.FinalText)
			evidenceCancel() // done with the evidence phase; free it before verifying

			// HARD GATE, evaluated BEFORE spending a cent on the verifier: a
			// project that defines its own checks (.moa/verify.json) cannot be
			// declared done while those checks are red. The checks are free (we
			// already ran them for the evidence), so when they're failing we
			// settle the iteration deterministically and SKIP the LLM verifier
			// entirely — paying the model to judge completeness on top of a broken
			// build would just be burning money to discard its verdict. Projects
			// without a verify.json have no deterministic gate and fall through to
			// the verifier as before.
			var verdict goal.Verdict
			var stats goal.VerifyStats
			var err error
			if checkGate.hasConfig && !checkGate.allPass {
				verdict = goal.Verdict{
					Satisfied: false,
					Feedback:  "Automated checks (.moa/verify.json) are NOT green, so the objective is not complete regardless of how the work looks. Fix them first:\n\n" + checkGate.summary,
				}
			} else {
				// Clamp the verifier's own budget so the loop's cumulative spend
				// can't blow the goal's total budget: cap it at whatever pool
				// remains, up to the per-run default.
				verifyBudget := goal.DefaultVerifierMaxBudget
				if info.TotalBudget > 0 {
					remaining := info.TotalBudget - sctx.Goal.Spent()
					if remaining <= 0 {
						stopGoal(sctx, "reached total budget")
						return
					}
					if remaining < verifyBudget {
						verifyBudget = remaining
					}
				}
				verdict, stats, err = goal.Verify(verifyCtx, goal.VerifyConfig{
					Factory:       sctx.ProviderFactory,
					VerifierSpec:  info.VerifierSpec,
					Objective:     info.Objective,
					Evidence:      evidence,
					PriorFeedback: summarizePriorVerdicts(sctx.Goal.PriorVerdicts()),
					StatePath:     info.StatePath,
					WorkDir:       goalWorkDir(sctx, info),
					Timeout:       info.VerifyTimeout,
					MaxBudget:     verifyBudget,
					OneShot:       info.VerifyOneShot,
				})
			}
			// Charge whatever the verifier spent against the goal budget, before
			// judging the verdict, so the ceiling holds even on the winning
			// iteration.
			spent := sctx.Goal.AddSpent(stats.CostUSD)

			// The verifier's spend is real LLM cost — surface it in the session
			// total too (TUI statusline + web usage widget), not only the goal
			// budget. RunEnded/SubagentEnded don't cover it: this is a separate
			// agentic call outside the maker run.
			if stats.CostUSD > 0 {
				total := sctx.addSessionCost(stats.CostUSD)
				sctx.Bus.Publish(SessionCostUpdated{SessionID: sctx.SessionID, TotalUSD: total, RunUSD: stats.CostUSD})
			}

			// If our verify context was cancelled, a user prompt or /goal stop
			// aborted us (cancelGoalVerify cancels both phases via `combined`).
			// That's not a verifier failure — bail silently so we don't spuriously
			// pause the goal or relaunch. Checked before the RunGen guard because a
			// user prompt cancels us *before* startRun bumps RunGen, so RunGen
			// alone wouldn't catch it. (evidenceCtx is always cancelled here — we
			// cancel it explicitly above — so only verifyCtx is meaningful.)
			if verifyCtx.Err() != nil {
				return
			}
			// Discard if a newer run started while we were verifying.
			if sctx.RunGenAtomic.Load() != startRunGen {
				return
			}
			// The goal may have been stopped (user ExitGoal, a backstop) while the
			// verifier was in flight. Don't judge or relaunch a goal that's over.
			if !sctx.Goal.Active() {
				return
			}
			if err != nil {
				// A verifier failure is infrastructure noise, NOT a "not satisfied"
				// verdict. goal.Verify already retried transient errors; if it
				// still failed, pause the loop (stop the goal, like an errored run)
				// instead of relaunching the maker with a cryptic, unactionable
				// error as "feedback". A user can inspect and re-issue /goal.
				sctx.Bus.Publish(GoalIterationEnded{
					SessionID: sctx.SessionID,
					Iteration: it,
					Satisfied: false,
					Feedback:  "verifier unavailable: " + err.Error(),
					Err:       err,
				})
				appendGoalMarker(sctx, goalIterationMarkerText(it, false, "verifier unavailable: "+err.Error()), map[string]any{
					"phase":     "iteration",
					"iteration": it,
					"satisfied": false,
				})
				stopGoal(sctx, "verifier unavailable (paused): "+err.Error())
				return
			}

			// Record this iteration's verdict so the next verification starts with
			// memory of what was already found lacking, instead of judging cold.
			sctx.Goal.RecordVerdict(it, verdict.Satisfied, verdict.Feedback)

			sctx.Bus.Publish(GoalIterationEnded{
				SessionID: sctx.SessionID,
				Iteration: it,
				Satisfied: verdict.Satisfied,
				Feedback:  verdict.Feedback,
			})
			appendGoalMarker(sctx, goalIterationMarkerText(it, verdict.Satisfied, verdict.Feedback), map[string]any{
				"phase":     "iteration",
				"iteration": it,
				"satisfied": verdict.Satisfied,
			})

			if verdict.Satisfied {
				stopGoal(sctx, "objective met")
				return
			}

			// The verifier's spend may have exhausted the goal's total budget.
			// Stop now rather than relaunch a maker iteration we can't pay for.
			if info.TotalBudget > 0 && spent >= info.TotalBudget {
				stopGoal(sctx, "reached total budget")
				return
			}

			// Not satisfied — relaunch, but guard against a spin loop. "Stalled"
			// means the iteration made no forward progress (no file edits and no
			// new commit), NOT merely that the global objective isn't finished: a
			// long goal is legitimately "not done" for many productive iterations.
			var commit string
			if dir := goalWorkDir(sctx, info); dir != "" {
				commit = runGit(verifyCtx, dir, "rev-parse", "HEAD")
			}
			progressed := e.HadEdits || (commit != "" && commit != sctx.Goal.LastCommit())
			sctx.Goal.SetLastCommit(commit)
			if progressed {
				sctx.Goal.ResetStalled()
			} else {
				stalled := sctx.Goal.IncStalled()
				if info.MaxStalled > 0 && stalled >= info.MaxStalled {
					stopGoal(sctx, fmt.Sprintf("no progress after %d attempts", stalled))
					return
				}
			}
			// Stop here if we've verified the last allowed iteration — checking
			// after the verdict means all N iterations are actually verified
			// (checking before relaunch would run an N+1th, unverified run).
			if info.MaxIterations > 0 && it >= info.MaxIterations {
				stopGoal(sctx, fmt.Sprintf("reached max iterations (%d)", info.MaxIterations))
				return
			}
			// The deadline may have passed while building evidence + verifying
			// (both can take minutes). Re-check before relaunching so a goal can't
			// overshoot --timeout by a whole extra iteration.
			if !info.Deadline.IsZero() && time.Now().After(info.Deadline) {
				stopGoal(sctx, "reached time limit")
				return
			}
			// Cap the next iteration at the remaining budget so the loop's total
			// spend stays under the ceiling (the agent resets per-run cost each
			// run). spent < TotalBudget here — the equal-or-over case stopped above.
			if info.TotalBudget > 0 {
				remaining := info.TotalBudget - sctx.Goal.Spent()
				if err := sctx.Agent.SetMaxBudget(remaining); err != nil {
					fmt.Fprintf(os.Stderr, "warning: goal budget cap: %v\n", err)
				}
			}
			feedback := strings.TrimSpace(verdict.Feedback)
			if feedback == "" {
				feedback = "The objective is not yet satisfied. Re-check it against your STATE.md and the actual diff, then continue."
			}
			goalRelaunch(sctx, "Not done yet.\n\n"+feedback+"\n\nContinue.")
		}()
	})
}

func steerAttachmentIDs(items []core.SteerItem) []string {
	var ids []string
	for _, item := range items {
		for _, content := range item.Content {
			if content.AttachmentID != "" {
				ids = append(ids, content.AttachmentID)
			}
		}
	}
	return ids
}

// goalIterationMarkerText formats an iteration verdict for the persistent goal
// marker, matching the wording the frontends use for the live event so the
// in-memory render and the reloaded render read identically.
func goalIterationMarkerText(iteration int, satisfied bool, feedback string) string {
	verdict := "not done yet"
	if satisfied {
		verdict = "satisfied"
	}
	text := fmt.Sprintf("🎯 Goal iteration %d — %s", iteration, verdict)
	if fb := strings.TrimSpace(feedback); fb != "" {
		text += "\n" + fb
	}
	return text
}

// appendGoalMarker records a goal-lifecycle event (start, iteration verdict,
// end) as a persistent marker message in the conversation so it survives a
// reload — the live GoalChanged/GoalIterationEnded/GoalEnded events are only
// rendered in-memory by the frontends and are lost on reopen.
//
// The marker uses role "goal", which IsLLMMessage/isLLMRole exclude, so it never
// enters the LLM context (same approach as role "shell"). It is appended via
// AppendMessage and followed by a CommandExecuted{Command:"goal"} publish so the
// TreeSyncer persists it and the web frontend receives the refreshed history.
//
// AppendMessage is rejected while a run is live (e.g. a start marker fired from
// EnterGoal's first kick, or /goal stop mid-turn). In that case the append is
// deferred to the next RunEnded, when the agent is idle again.
func appendGoalMarker(sctx *SessionContext, text string, custom map[string]any) {
	c := map[string]any{"goal": true}
	for k, v := range custom {
		c[k] = v
	}
	msg := core.AgentMessage{
		Message: core.Message{
			Role:      "goal",
			Content:   []core.Content{core.TextContent(text)},
			Timestamp: time.Now().Unix(),
		},
		Custom: c,
	}
	publish := func() {
		// Refreshed history lets the web re-render; TreeSynced (from the
		// CommandExecuted re-sync) drives persistence.
		sctx.Bus.Publish(CommandExecuted{
			SessionID: sctx.SessionID,
			Command:   "goal",
			Messages:  sctx.Agent.Messages(),
		})
	}
	if err := sctx.Agent.AppendMessage(msg); err != nil {
		// Busy: defer to the next RunEnded, when the agent is idle again. The
		// RunEnded handler may fire on another goroutine the instant Subscribe
		// registers it — before the returned unsub is stored. Guard the
		// append+publish with sync.Once (runs exactly once), and record that the
		// handler fired; whichever side observes both "fired" and a stored unsub
		// performs the teardown, so the subscription never leaks and never
		// double-unsubscribes.
		var (
			mu    sync.Mutex
			fired bool
			unsub func()
		)
		tearDown := func() {
			// caller holds mu
			if fired && unsub != nil {
				u := unsub
				unsub = nil
				u()
			}
		}
		handler := func(e RunEnded) {
			mu.Lock()
			alreadyFired := fired
			fired = true
			mu.Unlock()
			if !alreadyFired {
				if appendErr := sctx.Agent.AppendMessage(msg); appendErr == nil {
					publish()
				}
			}
			mu.Lock()
			tearDown()
			mu.Unlock()
		}
		u := sctx.Bus.Subscribe(handler)
		mu.Lock()
		unsub = u
		tearDown() // in case the handler already fired before u was stored
		mu.Unlock()
		return
	}
	publish()
}

// stopGoal ends goal mode: it exits the Goal (which removes the directive via
// onChange), restores the previous compaction threshold, and announces the
// reason.
//
// Config mutations (system prompt, CompactAt) are rejected while a run is live —
// which happens when the user runs /goal stop mid-turn. In that case we defer
// the restore to the run's RunEnded, at which point the agent is idle again and
// the mutations succeed. Otherwise the directive and lowered threshold would
// leak into subsequent normal turns.
func stopGoal(sctx *SessionContext, reason string) {
	prev := sctx.goalPrevCompactAt
	prevBudget := sctx.goalPrevMaxBudget
	// Exit reports whether this call actually turned the goal off. If it was
	// already off (e.g. a TOCTOU with /goal stop), do nothing — otherwise we'd
	// publish a second GoalEnded and restore CompactAt/MaxBudget twice.
	if !sctx.Goal.Exit() {
		return
	}
	// Abort any in-flight verification so stale build/tests don't run against a
	// fresh run's edits.
	if sctx.cancelGoalVerify != nil {
		sctx.cancelGoalVerify()
	}
	// Restore the per-run budget the driver lowered each iteration, alongside the
	// compaction threshold. Both are rejected while a run is live, so defer to
	// RunEnded in that case (e.g. /goal stop mid-turn).
	compactErr := sctx.Agent.SetCompactAt(prev)
	budgetErr := sctx.Agent.SetMaxBudget(prevBudget)
	if compactErr != nil || budgetErr != nil {
		var unsub func()
		unsub = sctx.Bus.Subscribe(func(e RunEnded) {
			_ = sctx.Agent.SetCompactAt(prev)
			_ = sctx.Agent.SetMaxBudget(prevBudget)
			rebuildSystemPrompt(sctx) // re-apply now that the goal directive is gone
			unsub()
		})
	}
	sctx.Bus.Publish(GoalEnded{SessionID: sctx.SessionID, Reason: reason})
	appendGoalMarker(sctx, "🎯 Goal ended: "+reason, map[string]any{
		"phase":  "end",
		"reason": reason,
	})
}

// resolveGoalWorkDir validates and resolves EnterGoal's --cwd override. An
// empty cmdWorkDir keeps the existing behavior (evaluate in the session's
// CWD). A relative override resolves against the session CWD; the result must
// exist, be a directory, and pass the session's PathPolicy — otherwise
// verify.Execute (which runs the target directory's .moa/verify.json) would
// become a way to run arbitrary commands outside the sandbox. The error is
// actionable: it tells the user to `/path add` the directory first.
func resolveGoalWorkDir(sctx *SessionContext, cmdWorkDir string) (string, error) {
	if strings.TrimSpace(cmdWorkDir) == "" {
		return sctx.CWD, nil
	}
	dir := cmdWorkDir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(sctx.CWD, dir)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("goal: --cwd %q: %w", cmdWorkDir, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("goal: --cwd %q: %w", cmdWorkDir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("goal: --cwd %q is not a directory", cmdWorkDir)
	}
	if sctx.PathPolicy != nil && !sctx.PathPolicy.IsAllowed(real) {
		return "", fmt.Errorf("goal: --cwd %q is outside the allowed paths — run `/path add %s` first", cmdWorkDir, real)
	}
	return real, nil
}

// goalWorkDir returns the directory the driver should evaluate/execute in for
// the given goal snapshot: Info.WorkDir if set, else the session CWD. Kept as
// a helper so all four evaluation points (evidence, baseline commit, progress
// check, verify config) agree on the same resolution rule.
func goalWorkDir(sctx *SessionContext, info goal.Info) string {
	if info.WorkDir != "" {
		return info.WorkDir
	}
	return sctx.CWD
}

// goalRelaunch sends the next iteration's prompt if the agent is idle/error.
// Drops it if the goal is no longer active or a run is already in flight (a
// newer user turn took over).
func goalRelaunch(sctx *SessionContext, text string) {
	if sctx.Goal == nil || !sctx.Goal.Active() {
		return
	}
	if sctx.State != nil {
		if current := sctx.State.Current(); current != StateIdle && current != StateError {
			return
		}
	}
	_ = sctx.Bus.Execute(SendPrompt{
		SessionID: sctx.SessionID,
		Text:      text,
		Custom:    map[string]any{"source": "goal"},
	})
}

// goalChangedEvent builds a GoalChanged event from a goal Info snapshot.
func goalChangedEvent(sessionID string, info goal.Info) GoalChanged {
	return GoalChanged{
		SessionID: sessionID,
		Active:    info.Active,
		Objective: info.Objective,
		WorkDir:   info.WorkDir,
		Iteration: info.Iteration,
		Stalled:   info.Stalled,
	}
}

func goalFirstKick(info goal.Info) string {
	if info.WorkDir != "" {
		return fmt.Sprintf("Start the goal. Work in %s — read %s there, then work the objective: %s", info.WorkDir, info.StatePath, info.Objective)
	}
	return fmt.Sprintf("Start the goal. Read %s, then work the objective: %s", info.StatePath, info.Objective)
}

// summarizePriorVerdicts condenses earlier iterations' verdicts into a compact
// memo for the next verification, so the verifier doesn't judge each iteration
// cold. Only unsatisfied verdicts carry actionable "what was missing" feedback;
// a satisfied line would only appear if a later gate reopened the goal. Returns
// "" when there's nothing to report.
func summarizePriorVerdicts(verdicts []goal.IterationVerdict) string {
	if len(verdicts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, v := range verdicts {
		status := "not satisfied"
		if v.Satisfied {
			status = "satisfied"
		}
		fmt.Fprintf(&b, "- Iteration %d: %s", v.Iteration, status)
		if fb := strings.TrimSpace(v.Feedback); fb != "" {
			b.WriteString("\n  ")
			// Indent multi-line feedback so the list stays readable.
			b.WriteString(strings.ReplaceAll(fb, "\n", "\n  "))
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// goalCheckGate captures the deterministic outcome of the project's own checks
// (.moa/verify.json) for one verification. The driver uses it as a hard gate:
// when a project defines checks and they are red (or didn't run), the goal
// cannot be declared satisfied no matter what the LLM verdict says.
type goalCheckGate struct {
	hasConfig bool   // the project has a .moa/verify.json
	allPass   bool   // every defined check passed (meaningful only if hasConfig)
	summary   string // human-readable check output, for maker feedback on failure
}

// buildGoalEvidence assembles the verifier's evidence: the maker's final text
// plus the current git state (diff stat + last commit), so the verifier can see
// whether work was actually committed. It also runs the project's checks once
// and returns their outcome as a gate the driver enforces deterministically.
// Kept short and best-effort.
func buildGoalEvidence(ctx context.Context, cwd, finalText string) (string, goalCheckGate) {
	var b strings.Builder
	var gate goalCheckGate
	if strings.TrimSpace(finalText) != "" {
		b.WriteString("WORKER'S FINAL MESSAGE:\n")
		b.WriteString(finalText)
		b.WriteString("\n\n")
	}
	if cwd != "" {
		status := runGit(ctx, cwd, "status", "--short")
		if status != "" {
			b.WriteString("UNCOMMITTED CHANGES (git status --short):\n")
			b.WriteString(status)
			b.WriteString("\n")
		}
		if out := runGit(ctx, cwd, "log", "-1", "--format=%h %s"); out != "" {
			b.WriteString("LAST COMMIT:\n")
			b.WriteString(out)
			b.WriteString("\n")
		}
		// The actual change content, not just file names — so the verifier can
		// judge whether the diff really implements the objective instead of
		// trusting the worker's self-report.
		if out := runGit(ctx, cwd, "diff", "HEAD"); out != "" {
			b.WriteString("\nDIFF vs HEAD (working tree + staged):\n")
			b.WriteString(out)
			b.WriteString("\n")
		} else if status == "" {
			// Clean tree: the maker committed its work (the directive tells it
			// to). `git diff HEAD` is then empty, which would leave the verifier
			// with almost no evidence and bias it toward "not satisfied". Show
			// the last commit's own diff instead.
			if out := runGit(ctx, cwd, "show", "--stat", "-p", "HEAD"); out != "" {
				b.WriteString("\nLAST COMMIT DIFF (git show HEAD):\n")
				b.WriteString(out)
				b.WriteString("\n")
			}
		}
		// Objective evidence: actually run the project's checks (build/tests).
		// A worker claiming "all tests pass" no longer settles it — the verifier
		// sees the real result. Absent a verify config, say so plainly.
		res, err := verify.Execute(ctx, cwd)
		switch {
		case errors.Is(err, verify.ErrNoConfig):
			// No checks defined: no deterministic gate, the verifier decides.
			b.WriteString("\nAUTOMATED CHECKS: not run (no .moa/verify.json)\n")
		case err != nil:
			// The config exists but couldn't be loaded/ran (invalid JSON, ctx
			// cancelled, …). Treat as a red gate: a project that defines checks
			// must not be declared done while they can't be shown green.
			gate.hasConfig = true
			gate.allPass = false
			gate.summary = "checks could not be run: " + err.Error()
			b.WriteString("\nAUTOMATED CHECKS: not run (" + err.Error() + ")\n")
		default:
			gate.hasConfig = true
			gate.allPass = res.AllPass
			gate.summary = verify.FormatResult(res)
			b.WriteString("\nAUTOMATED CHECKS (build/tests):\n")
			b.WriteString(gate.summary)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String()), gate
}

// runGit runs a read-only git command in dir and returns trimmed, length-capped
// stdout. Returns "" on any error (not a git repo, git missing, etc.).
func runGit(ctx context.Context, dir string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	const maxLen = 4000
	if len(s) > maxLen {
		s = s[:maxLen] + "\n…(truncated)"
	}
	return s
}

// startRun is the shared implementation for SendPrompt and SendPromptWithContent.
// It validates state, creates a per-run context, and spawns the agent run goroutine.
func startRun(sctx *SessionContext, label string, runFn func(ctx context.Context) ([]core.AgentMessage, error)) error {
	if err := reserveRunSlot(sctx); err != nil {
		return err
	}
	launchRun(sctx, label, runFn)
	return nil
}

// reserveRunSlot transitions the session idle/error → running, claiming the run
// slot without launching anything. Split out of startRun so the queue pump can
// reserve the slot BEFORE it drains steers from the queue (reserve-then-drain):
// that closes the window where a concurrent SendPrompt could see an empty queue
// plus idle state and start a run that jumps ahead of the queued steers.
// Returns an error if the session is not in a startable state.
func reserveRunSlot(sctx *SessionContext) error {
	if sctx.State != nil {
		if err := sctx.State.Transition(StateRunning); err != nil {
			return fmt.Errorf("cannot send: %w", err)
		}
	}
	return nil
}

func resetRunTokens(sctx *SessionContext, runGen uint64) {
	sctx.runTokenMu.Lock()
	if runGen <= sctx.runTokensGen {
		sctx.runTokenMu.Unlock()
		return
	}
	sctx.runTokenMu.Unlock()

	baseline := len(sctx.Agent.Messages())
	sctx.runTokenMu.Lock()
	defer sctx.runTokenMu.Unlock()
	if runGen <= sctx.runTokensGen {
		return
	}
	sctx.runTokenBaseline = baseline
	sctx.runTokensUp = 0
	sctx.runTokensDown = 0
	sctx.runTokensGen = runGen
}

// launchRun starts the agent goroutine for a slot already reserved by
// reserveRunSlot. It creates the per-run context, publishes RunStarted, and runs
// runFn in a goroutine, settling the state and publishing RunEnded when it ends.
func launchRun(sctx *SessionContext, label string, runFn func(ctx context.Context) ([]core.AgentMessage, error)) {
	// Create per-run context with generation token.
	sctx.runMu.Lock()
	runCtx, gen := sctx.newRunContext()
	sctx.runMu.Unlock()
	// Establish the baseline before the agent goroutine can append the run's
	// first user message. The RunStarted reactor observes this same generation
	// and is a no-op, while direct RunStarted publishers still reset it there.
	resetRunTokens(sctx, gen)

	// Notify subscribers of the run generation (single source of truth for runGen).
	sctx.Bus.Publish(RunStarted{SessionID: sctx.SessionID, RunGen: gen})

	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Convert panics into a settled run rather than stranding StateRunning.
				err := fmt.Errorf("run panic: %v", r)
				if sctx.Checkpoints != nil {
					sctx.Checkpoints.Discard()
				}
				sctx.setCompacting(false)
				sctx.clearRunCancel(gen)
				if sctx.State != nil {
					_ = sctx.State.TransitionWithError(StateError, err.Error())
				}
				sctx.Bus.Publish(RunEnded{SessionID: sctx.SessionID, RunGen: gen, Err: err})
			}
		}()
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

		// Snapshot before returning to idle: an immediately-started later run
		// resets the generation accumulator, but must never erase this result.
		stats := sctx.snapshotRunStats(gen)

		// Clear run cancel BEFORE state transition to prevent a race where
		// a new run starts (setting a new runCancel) and then this goroutine
		// clears it. The generation token ensures we only clear our own cancel.
		sctx.clearRunCancel(gen)

		// State transition.
		if sctx.State != nil {
			if err != nil && !cancelled {
				_ = sctx.State.TransitionWithError(StateError, cleanRunError(err))
			} else {
				_ = sctx.State.Transition(StateIdle)
			}
		}

		// Controllers used by integrations may return messages without emitting
		// lifecycle events. Keep text/edit compatibility fallbacks only; cost
		// remains lifecycle-attributed so compaction cannot mischarge history.
		if stats.finalText == "" {
			stats.finalText = extractFinalAssistantText(msgs)
		}
		if !stats.hadEdits {
			stats.hadEdits = hasSuccessfulEdits(msgs)
		}

		// Publish run result.
		var runErr error
		if err != nil && !cancelled {
			runErr = err
		}
		sctx.Bus.Publish(RunEnded{
			SessionID: sctx.SessionID,
			RunGen:    gen,
			FinalText: stats.finalText,
			Err:       runErr,
			HadEdits:  stats.hadEdits,
			Cost:      stats.costUSD,
		})
	}()
}

// cleanRunError renders a run error for user-facing display. It unwraps the
// internal "stream: provider: …" plumbing prefixes and, for a usage/quota
// limit, uses the typed error's clean message ("… quota exceeded: … (resets in
// X)") so the user sees an actionable reason instead of raw HTTP noise or —
// worse — a false "interrupted" label.
func cleanRunError(err error) string {
	if err == nil {
		return ""
	}
	if qe, ok := core.AsQuotaExceeded(err); ok {
		return qe.Error()
	}
	msg := err.Error()
	for _, prefix := range []string{"stream: ", "provider: "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return msg
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
