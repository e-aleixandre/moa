// handlers_run.go contains bus handlers for the corresponding session concerns.

package bus

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/e-aleixandre/moa/pkg/core"
)

type handlerSharedState struct {
	autoVerifyCount  atomic.Int32
	autoVerifyCancel atomic.Pointer[context.CancelFunc]
}

func newHandlerSharedState() *handlerSharedState { return &handlerSharedState{} }

func (s *handlerSharedState) cancelAutoVerify() {
	if fn := s.autoVerifyCancel.Swap(nil); fn != nil {
		(*fn)()
	}
}

func registerRuntimeSubscriptions(sctx *SessionContext) {
	b := sctx.Bus

	// Background jobs can continue after the foreground agent reaches idle.
	// Keep their lifecycle in the runtime so headless callers can wait for the
	// same complete result chain that interactive frontends observe.
	b.Subscribe(func(e SubagentStarted) { sctx.trackBackgroundEvent(e) })
	b.Subscribe(func(e SubagentEnded) { sctx.trackBackgroundEvent(e) })
	b.Subscribe(func(e BashJobStarted) { sctx.trackBackgroundEvent(e) })
	b.Subscribe(func(e BashJobSettled) { sctx.trackBackgroundEvent(e) })

	// A message ID is claimed only while its send is in flight. The
	// announcement is published from the append point, so by the time it
	// arrives the message is in history and history alone keeps the ID taken:
	// drop the claim, otherwise the claim map would grow for the whole life of
	// the session (see reserveMsgID).
	b.Subscribe(func(e UserMessageAppended) { sctx.releaseMsgID(e.MsgID) })

}

func registerRunControlHandlers(sctx *SessionContext) {
	b := sctx.Bus

	abortRun := func(expectedGen uint64, discardedOut *[]core.SteerItem) error {
		sctx.abortMu.Lock()
		defer sctx.abortMu.Unlock()
		if expectedGen != 0 && sctx.RunGenAtomic.Load() != expectedGen {
			return ErrSessionBusy
		}
		// Agent.Abort takes the same lock as the post-tool delivery boundary, so
		// no queued steer can cross into history after Stop has claimed it.
		sctx.Agent.Abort()
		sctx.cancelRun()
		// Claim the remaining queue after cancellation. A steer that already
		// crossed into history is not returned here, so clients must not restore
		// and send it a second time after Stop.
		discarded := sctx.Agent.CancelSteer()
		if discardedOut != nil {
			*discardedOut = discarded
		}
		sctx.Bus.Publish(SteersCanceled{
			SessionID:     sctx.SessionID,
			AttachmentIDs: steerAttachmentIDs(discarded),
		})
		return nil
	}
	b.OnCommand(func(AbortRun) error {
		return abortRun(0, nil)
	})
	b.OnCommand(func(cmd AbortAndRecall) error {
		return abortRun(cmd.RunGen, cmd.DiscardedSteers)
	})

	b.OnCommand(func(cmd SteerAgent) error {
		sctx.abortMu.Lock()
		defer sctx.abortMu.Unlock()
		// Centralize the ID invariant: every queued steer has a stable ID even
		// if a caller (CLI, internal) forgot to mint one.
		if cmd.ID == "" {
			cmd.ID = core.NewSteerID()
		}
		if err := trySteer(sctx.Agent, core.SteerItem{ID: cmd.ID, Text: cmd.Text, Custom: cmd.Custom, Content: cmd.Content, Internal: cmd.Internal}); err != nil {
			return err
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
		sctx.abortMu.Lock()
		defer sctx.abortMu.Unlock()
		if cmd.ID == "" {
			cmd.ID = core.NewSteerID()
		}
		// A barrier carries the raw command line in both Command (the executable
		// form) and Text (display). It is never injected as a message.
		if err := trySteer(sctx.Agent, core.SteerItem{ID: cmd.ID, Text: cmd.Raw, Command: cmd.Raw}); err != nil {
			return err
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
}

func registerCancelSteerHandler(sctx *SessionContext) {
	b := sctx.Bus
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
}

func registerRunPromptHandlers(sctx *SessionContext, shared *handlerSharedState) {
	b := sctx.Bus
	autoVerifyCount := &shared.autoVerifyCount
	cancelAutoVerify := shared.cancelAutoVerify
	b.OnCommand(func(cmd SendPrompt) error {
		sctx.abortMu.Lock()
		defer sctx.abortMu.Unlock()
		// Strict-order gate (INV-2): a genuine user prompt must not start a run
		// while the queue rail holds pending items — it would jump ahead of a
		// queued barrier/steer. Convert it into a steer at the tail of the queue
		// instead, preserving send order; the pump delivers it in turn. Internal
		// producers (the goal loop, auto-verify) are exempt: they are the
		// machinery the queue is waiting on, not new user turns, and steering
		// them would strip their Custom source.
		if !isInternalPromptSource(cmd.Custom) && sctx.Agent.QueueLen() > 0 {
			id := cmd.SteerID
			if id == "" {
				id = core.NewSteerID()
			}
			if err := trySteer(sctx.Agent, core.SteerItem{ID: id, Text: cmd.Text, Custom: cmd.Custom}); err != nil {
				return err
			}
			// Report the identity on the STEER rail: a chip ID must never be
			// reported as a message ID, or the caller reconciles the wrong rail
			// (the chip would be dropped and a phantom message kept).
			if cmd.AcceptedSteerID != nil {
				*cmd.AcceptedSteerID = id
			}
			if cmd.AcceptedMsgID != nil {
				*cmd.AcceptedMsgID = ""
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
		// Pre-mint the user message's ID so the prompt is announced live
		// (UserMessageAppended, emitted by the agent when the message actually
		// lands in history) under the same identity clients dedup by. Prompts
		// carrying Custom metadata are usually internal producers (goal/auto-
		// verify/schedule) or notifications (subagent/bash) with their own
		// rendering. Secret batches are the exception: their trusted metadata
		// drives the live secret card.
		//
		// The claim happens HERE, at the point the run is accepted, not in the
		// caller: only an atomic check-and-claim keeps two concurrent sends with
		// the same client-supplied ID from both landing under it (see
		// reserveMsgID). The effective ID goes back through AcceptedMsgID.
		msgID := cmd.MsgID
		if cmd.Custom == nil {
			msgID = sctx.reserveMsgID(msgID)
			if cmd.AcceptedMsgID != nil {
				*cmd.AcceptedMsgID = msgID
			}
		}
		if err := startRun(sctx, cmd.Text, func(ctx context.Context) ([]core.AgentMessage, error) {
			if cmd.Custom != nil {
				switch cmd.Custom["source"] {
				case "secret_batch", "event":
					return sctx.Agent.SendWithCustomAnnounced(ctx, cmd.Text, cmd.Custom)
				}
				return sctx.Agent.SendWithCustom(ctx, cmd.Text, cmd.Custom)
			}
			if msgID != "" {
				return sctx.Agent.SendWithMsgID(ctx, cmd.Text, msgID)
			}
			return sctx.Agent.Send(ctx, cmd.Text)
		}); err != nil {
			// The prompt never ran, so its ID was never shown as delivered:
			// give it back rather than burning it for the session.
			if cmd.Custom == nil {
				sctx.releaseMsgID(msgID)
			}
			return err
		}
		return nil
	})

	b.OnCommand(func(cmd SendPromptWithContent) error {
		sctx.abortMu.Lock()
		defer sctx.abortMu.Unlock()
		// Strict-order gate (INV-2): queue behind pending items instead of
		// jumping ahead. Content sends are always user-initiated, so no source
		// exemption applies.
		if sctx.Agent.QueueLen() > 0 {
			id := cmd.SteerID
			if id == "" {
				id = core.NewSteerID()
			}
			// Carry the plain text alongside the content blocks: the Steered
			// event (and every client rendering it) shows Text, so a queued
			// send with attachments would otherwise surface as an empty chip.
			if err := trySteer(sctx.Agent, core.SteerItem{ID: id, Text: contentText(cmd.Content), Content: cmd.Content}); err != nil {
				return err
			}
			if cmd.AcceptedSteerID != nil {
				*cmd.AcceptedSteerID = id
			}
			if cmd.AcceptedMsgID != nil {
				*cmd.AcceptedMsgID = ""
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
		// Claim the message identity atomically with accepting the run — see
		// the SendPrompt handler.
		msgID := sctx.reserveMsgID(cmd.MsgID)
		if cmd.AcceptedMsgID != nil {
			*cmd.AcceptedMsgID = msgID
		}
		if err := startRun(sctx, label, func(ctx context.Context) ([]core.AgentMessage, error) {
			return sctx.Agent.SendWithContentAnnounced(ctx, cmd.Content, msgID)
		}); err != nil {
			sctx.Agent.ReleaseNativeDocBytes(nativeBytes)
			sctx.releaseMsgID(msgID)
			return err
		}
		return nil
	})

}

func registerAppendHandler(sctx *SessionContext) {
	b := sctx.Bus
	b.OnCommand(func(cmd AppendToConversation) error {
		return sctx.Agent.AppendMessage(cmd.Message)
	})

}

func registerRunReactors(sctx *SessionContext) {
	b := sctx.Bus
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
	// so every reader reports the same figure from one source of truth.
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
	launchRunWithSettled(sctx, label, runFn, nil)
}

// launchRunWithSettled behaves like launchRun and invokes settled after the
// state has settled but before publishing RunEnded.
func launchRunWithSettled(sctx *SessionContext, label string, runFn func(ctx context.Context) ([]core.AgentMessage, error), settled func(cancelled bool, err error)) {
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
				if settled != nil {
					settled(false, err)
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

		// Atomically close the AbortRun window before deciding the terminal
		// result. A Stop that won before this point cancels the handoff; one
		// after it cannot retroactively change a settled run.
		cancelled = cancelled || sctx.settleRunCancel(gen, runCtx)

		// State transition.
		if sctx.State != nil {
			if err != nil && !cancelled {
				_ = sctx.State.TransitionWithError(StateError, cleanRunError(err))
			} else {
				_ = sctx.State.Transition(StateIdle)
			}
		}
		sctx.clearRunStartedAt(gen)

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
		if settled != nil {
			settled(cancelled, runErr)
		}
		sctx.Bus.Publish(RunEnded{
			SessionID: sctx.SessionID,
			RunGen:    gen,
			FinalText: stats.finalText,
			Err:       runErr,
			Cancelled: cancelled,
			HadEdits:  stats.hadEdits,
			Cost:      stats.costUSD,
		})
	}()
}

// cleanRunError renders a run error for user-facing display. It unwraps the
// internal "stream: provider: …" plumbing prefixes and, for a usage/quota
// limit, uses the typed error's clean message ("… quota exceeded: … (resets in
// X)") so the user sees an actionable reason instead of raw HTTP noise or —
