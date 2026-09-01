// handlers_history.go contains bus handlers for the corresponding session concerns.

package bus

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/handoff"
	"github.com/e-aleixandre/moa/pkg/session"
	"github.com/e-aleixandre/moa/pkg/tasks"
)

func registerHistoryHandlers(sctx *SessionContext) {
	b := sctx.Bus
	// Serializes the short start and terminal sections of manual compactions.
	// The model call itself deliberately runs without this lock. Without the
	// lock, the idle transition at the end can admit a second compact before the
	// first one clears the compacting flag and publishes its terminal event.
	var compactionLifecycleMu sync.Mutex
	b.OnCommand(func(cmd ClearSession) error {
		if sctx.treeSyncer != nil {
			if err := sctx.treeSyncer.ResetAndClear(); err != nil {
				return err
			}
		} else {
			sctx.historyMu.Lock()
			if err := sctx.Agent.Reset(); err != nil {
				sctx.historyMu.Unlock()
				return err
			}
			if sctx.Tree != nil {
				sctx.Tree.Clear()
			}
			sctx.historyMu.Unlock()
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
		compactionLifecycleMu.Lock()
		defer compactionLifecycleMu.Unlock()
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
		// events.
		sctx.setCompacting(true)
		sctx.Bus.Publish(CompactionStarted{SessionID: sctx.SessionID})
		// The expensive part (a model call taking tens of seconds) runs on its
		// own goroutine so the caller — an HTTP POST — gets an
		// ACCEPTANCE, not a completion: a returned nil means "compaction
		// started", and the outcome arrives as events. Everything that claims
		// the session (the running transition, the compacting flag and
		// CompactionStarted) already happened synchronously above, which is what
		// makes this safe: by the time this handler returns, a concurrent close
		// sees the session busy (DoIfQuiescent → State.DoIfIdle) and refuses to
		// tear the runtime down under the goroutine.
		go func() {
			var result *core.CompactionPayload
			var err error
			var messages []core.AgentMessage
			var marker *core.AgentMessage
			// This defer covers the complete asynchronous path, not only
			// Agent.Compact. A panic from any controller callback must still leave
			// the session settled and let queued work continue.
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("compaction panic: %v", r)
					result = nil
					messages = nil
					marker = nil
				}

				compactionLifecycleMu.Lock()
				defer compactionLifecycleMu.Unlock()
				// Settle the state BEFORE publishing results, mirroring startRun:
				// reactors observing CompactionEnded must see idle/error, not
				// running. Holding the lifecycle lock through the terminal event
				// prevents another compact from starting in this narrow interval.
				if sctx.State != nil {
					if err != nil {
						_ = sctx.State.TransitionWithError(StateError, err.Error())
					} else {
						_ = sctx.State.Transition(StateIdle)
					}
				}
				sctx.setCompacting(false)
				if err != nil {
					sctx.Bus.Publish(CompactionEnded{SessionID: sctx.SessionID, Err: err})
				} else {
					sctx.Bus.Publish(CompactionEnded{
						SessionID: sctx.SessionID,
						Payload:   result, // nil if nothing to compact
						Marker:    marker,
					})
					// Always publish CommandExecuted on success so persistence and frontends react.
					sctx.Bus.Publish(CommandExecuted{
						SessionID: sctx.SessionID,
						Command:   "compact",
						Messages:  messages,
					})
				}
				// Messages sent while the compact held the session busy were queued as
				// steers, but no run is coming to drain them — pump the queue now.
				requestPump(sctx)
			}()

			result, err = sctx.Agent.Compact(sctx.SessionCtx, cmd.Focus)
			if err == nil {
				messages = sctx.Agent.Messages()
				marker = NewCompactionMarker(result)
			}
		}()
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
			sctx.Bus.Publish(CompactionEnded{SessionID: sctx.SessionID, Payload: payload, Marker: NewCompactionMarker(payload)})
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

	b.OnCommand(func(cmd HandoffSession) error {
		if sctx.ProviderFactory == nil {
			return fmt.Errorf("handoff unavailable: provider factory not configured")
		}
		if err := reserveRunSlot(sctx); err != nil {
			return fmt.Errorf("cannot hand off: %w", err)
		}
		// Resolve omitted destination settings at execution time, so the
		// destination faithfully inherits the settled source configuration.
		sourceModel := sctx.Agent.Model()
		targetModelSpec := sourceModel.ID
		if sourceModel.Provider != "" {
			targetModelSpec = sourceModel.Provider + "/" + sourceModel.ID
		}
		targetThinking := sctx.Agent.ThinkingLevel()
		if cmd.Options.ModelSpec != "" {
			targetModelSpec = cmd.Options.ModelSpec
		}
		if cmd.Options.Thinking != "" {
			targetThinking = cmd.Options.Thinking
		}
		launchRunWithSettled(sctx, "handoff", func(ctx context.Context) ([]core.AgentMessage, error) {
			model := sctx.Agent.Model()
			provider, err := sctx.ProviderFactory(model)
			if err != nil {
				return nil, fmt.Errorf("handoff provider: %w", err)
			}
			summary, usage, err := handoff.Generate(ctx, provider, model, sctx.Agent.Messages())
			if err != nil {
				return nil, err
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if usage != nil {
				gen, up, down := sctx.addInternalRunUsage(usage, model.Pricing)
				sctx.Bus.Publish(RunTokensUpdated{SessionID: sctx.SessionID, RunGen: gen, Up: up, Down: down})
			}
			sctx.Bus.Publish(HandoffReady{
				SessionID: sctx.SessionID,
				Prompt:    handoff.Prompt(summary),
				ModelSpec: targetModelSpec,
				Thinking:  targetThinking,
			})
			return sctx.Agent.Messages(), nil
		}, func(cancelled bool, err error) {
			sctx.Bus.Publish(HandoffSettled{SessionID: sctx.SessionID, Cancelled: cancelled, Err: err})
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
}

func registerSessionQueryHandlers(sctx *SessionContext) {
	b := sctx.Bus
	b.OnQuery(func(q GetMessages) ([]core.AgentMessage, error) {
		return sctx.Agent.Messages(), nil
	})

	b.OnQuery(func(q GetModel) (core.Model, error) {
		return sctx.Agent.Model(), nil
	})

	b.OnQuery(func(q GetThinkingLevel) (string, error) {
		return sctx.Agent.ThinkingLevel(), nil
	})

	b.OnQuery(func(q GetCompactAt) (int, error) {
		return sctx.Agent.CompactAt(), nil
	})

	b.OnQuery(func(q GetEffectiveCompactAt) (int, error) {
		return sctx.Agent.EffectiveCompactAt(), nil
	})

	b.OnQuery(func(q GetCompactAtFloor) (int, error) {
		return sctx.Agent.CompactAtFloor(), nil
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

	b.OnQuery(func(q GetRunGeneration) (uint64, error) {
		return sctx.RunGenAtomic.Load(), nil
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

	b.OnQuery(func(q GetAutoVerifying) (bool, error) {
		return sctx.AutoVerifying(), nil
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

	b.OnQuery(func(q GetSessionState) (string, error) {
		if sctx.State == nil {
			return "idle", nil
		}
		return string(sctx.State.Current()), nil
	})
}

func registerTreeHandlers(sctx *SessionContext) {
	b := sctx.Bus
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
		sctx.historyMu.RLock()
		defer sctx.historyMu.RUnlock()
		if sctx.Tree != nil {
			if msgs := sctx.Tree.AllMessages(); len(msgs) > 0 {
				return msgs, nil
			}
		}
		return sctx.Agent.Messages(), nil
	})

	b.OnQuery(func(q GetDisplayMessagesSince) (DisplayMessagesSince, error) {
		if sctx.treeSyncer != nil {
			// The syncer dates the anchor from its own tree under its own lock;
			// re-reading sctx.Tree here would race the pointer swap on
			// clear/resume and could mix two trees in one answer.
			messages, valid, entryAt := sctx.treeSyncer.DisplayMessagesSince(q.EntryID)
			return DisplayMessagesSince{Messages: messages, Valid: valid, EntryAt: entryAt}, nil
		}
		sctx.historyMu.RLock()
		defer sctx.historyMu.RUnlock()
		// One read of the pointer, used for both the suffix and its anchor, so
		// this branch cannot mix trees either.
		if tree := sctx.Tree; tree != nil {
			messages, valid := tree.DisplayMessagesSince(q.EntryID)
			if !valid {
				return DisplayMessagesSince{}, nil
			}
			return DisplayMessagesSince{Messages: messages, Valid: true, EntryAt: anchorTimestamp(tree, q.EntryID)}, nil
		}
		return DisplayMessagesSince{}, nil
	})

	b.OnQuery(func(q MsgIDInUse) (bool, error) {
		if q.MsgID == "" {
			return false, nil
		}
		// Scan the display projection: it is the same history clients dedup
		// against (tree entries plus the in-flight turn), so an ID it already
		// contains is exactly one that would swallow a new message. Also
		// consider IDs claimed by an accepted send whose message has not been
		// appended yet — the answer is advisory (the send path re-checks under
		// the reservation lock), but reporting a claimed ID as free would let a
		// caller confirm an identity it is about to lose.
		if sctx.msgIDReserved(q.MsgID) {
			return true, nil
		}
		return sctx.msgIDInHistory(q.MsgID), nil
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

}
