package bus

import (
	"context"
	"errors"
	"strings"

	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/goal"
)

// requestPump asks the queue pump to run, coalescing concurrent requests into a
// single non-overlapping pump. If a pump is already active it records that
// another loop is needed (pumpRerun) — so an item enqueued mid-pump, or a
// barrier that itself ends a run, is never missed — and returns. Otherwise it
// launches the pump loop on its OWN goroutine.
//
// A dedicated goroutine (not the caller's) is required because, with the
// execute-then-pop model, a pump pass can block for the whole duration of a
// barrier command (a /compact takes seconds, a /verify minutes). The pump is
// triggered from event reactors (RunEnded, CompactionEnded) whose subscriber
// goroutines must not be held that long.
//
// Serialization here is CORRECTNESS, not just hygiene: barriers execute before
// they are popped, so two overlapping pumps could double-execute one.
func requestPump(sctx *SessionContext) {
	sctx.pumpMu.Lock()
	if sctx.pumpActive {
		sctx.pumpRerun = true
		sctx.pumpMu.Unlock()
		return
	}
	sctx.pumpActive = true
	sctx.pumpMu.Unlock()

	go pumpLoop(sctx)
}

// pumpLoop runs pump passes until no rerun was requested. pumpMu is never held
// across pumpOnce, so a reentrant requestPump (a barrier command that publishes
// an idle event handled synchronously via Bus.Execute) simply sets pumpRerun.
func pumpLoop(sctx *SessionContext) {
	for {
		pumpOnce(sctx)

		sctx.pumpMu.Lock()
		if sctx.pumpRerun {
			sctx.pumpRerun = false
			sctx.pumpMu.Unlock()
			continue
		}
		sctx.pumpActive = false
		sctx.pumpMu.Unlock()
		return
	}
}

// pumpOnce performs one drain pass over the unified queue rail while the session
// is idle. For each item at the head:
//   - a barrier command is EXECUTED first and only popped once its execution has
//     succeeded (execute-then-pop, INV-1): while it runs it stays at the head of
//     the queue, so a concurrent producer sees a non-empty queue and enqueues
//     behind it instead of jumping ahead;
//   - a run of leading steers reserves the run slot BEFORE draining them
//     (reserve-then-drain, INV-1), then launches one run and returns — that
//     run's RunEnded re-triggers the pump for whatever follows.
//
// It never blocks on a run completing; barrier commands run synchronously via
// Bus.Execute.
func pumpOnce(sctx *SessionContext) {
	for {
		// Only act when the session is idle/error. A run in flight owns the
		// drain and re-triggers the pump on its RunEnded. Error is treated like
		// idle: a barrier (e.g. /clear) can recover the session.
		if sctx.State != nil {
			if s := sctx.State.Current(); s != StateIdle && s != StateError {
				return
			}
		}

		// Abstain while goal mode is active: the goal driver owns the idle slot
		// between iterations (it relaunches from its own RunEnded reactor). A
		// barrier stealing the slot would break the goal loop. Queued barriers
		// wait until the goal ends or is stopped.
		if sctx.Goal != nil && sctx.Goal.Active() {
			return
		}

		head, ok := sctx.Agent.PeekQueueHead()
		if !ok {
			return // queue empty
		}

		if head.IsBarrier() {
			if done := pumpBarrier(sctx, head); !done {
				return // transient failure (lost slot): retry at next idle
			}
			continue
		}

		// Reserve the run slot BEFORE draining, so a concurrent SendPrompt can't
		// see empty-queue + idle and jump ahead of these steers.
		if err := reserveRunSlot(sctx); err != nil {
			return // a run started concurrently; its RunEnded re-triggers us
		}
		items := sctx.Agent.DrainUntilBarrier()
		if len(items) == 0 {
			// The steers were pulled back (CancelSteer) between peek and drain;
			// release the slot we reserved and stop.
			if sctx.State != nil {
				_ = sctx.State.Transition(StateIdle)
			}
			return
		}
		launchQueuedSteers(sctx, items)
		return // the run owns the rest; its RunEnded re-triggers the pump
	}
}

// pumpBarrier executes a barrier command at the head of the queue and, on
// success, pops it and announces CommandDequeued. It returns done=true when the
// pump should keep draining (the barrier is resolved, success or permanent
// failure) and done=false when it hit a TRANSIENT failure (it lost the run slot
// to a concurrent run) — in which case the barrier is left in the queue,
// unannounced, to be retried at the next idle point.
func pumpBarrier(sctx *SessionContext, head core.SteerItem) (done bool) {
	err := executeBarrier(sctx, head.Command)
	if isTransientBarrierErr(err) {
		// The barrier stays queued and un-popped; nothing announced.
		return false
	}
	// Resolved (ok or permanent failure): pop it if it is still the head (guards
	// a race with a concurrent pull-back), then announce.
	if !sctx.Agent.PopQueueBarrier(head.ID) {
		// Someone removed it under us (pull-back). Nothing to announce here; the
		// canceller already invalidated the chip.
		return true
	}
	ev := CommandDequeued{
		SessionID: sctx.SessionID,
		ID:        head.ID,
		Raw:       head.Command,
		Executed:  err == nil,
	}
	if err != nil {
		ev.Err = err.Error()
	}
	sctx.Bus.Publish(ev)
	return true
}

// isTransientBarrierErr reports whether a barrier failed only because it could
// not claim the run slot right now (a concurrent run is in flight). Such a
// barrier must be retried, not dropped. Detection is by typed sentinel only
// (ErrSessionBusy from RunManualVerify, ErrInvalidTransition wrapped by the
// state machine when a barrier's own idle→running transition fails) — never by
// matching error text, since a command's failure output (e.g. a /verify check
// summary) can contain arbitrary strings.
func isTransientBarrierErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrSessionBusy) || errors.Is(err, ErrInvalidTransition)
}

// launchQueuedSteers launches a run (slot already reserved) for steers that
// follow an executed barrier or were queued while a non-run operation held the
// session. Each item becomes its own user message (per-item MsgID, content
// preserved) and gets a Steered event moving its chip into the transcript.
func launchQueuedSteers(sctx *SessionContext, items []core.SteerItem) {
	if len(items) == 0 {
		return
	}
	// Pre-mint a stable MsgID per item so we can announce each chip immediately,
	// without waiting for the run goroutine. The message lands in state under
	// the same ID, so a reconnect snapshot dedups it by identity.
	msgIDs := make([]string, len(items))
	for i := range items {
		msgIDs[i] = core.NewMsgID()
	}
	launchRun(sctx, items[0].Text, func(ctx context.Context) ([]core.AgentMessage, error) {
		msgs, _, e := sctx.Agent.SendItems(ctx, items, msgIDs)
		return msgs, e
	})
	gen := sctx.RunGenAtomic.Load()
	for i, it := range items {
		if it.Internal {
			continue // internal steers have suppressed delivery events
		}
		sctx.Bus.Publish(Steered{
			SessionID: sctx.SessionID,
			RunGen:    gen,
			ID:        it.ID,
			MsgID:     msgIDs[i],
			Text:      it.Text,
		})
	}
}

// isInternalPromptSource reports whether a SendPrompt was issued by internal
// machinery (the goal loop or auto-verify) rather than a user turn. Such prompts
// are exempt from the strict-order queue gate: they are the operation the queue
// is waiting on, and converting them to steers would strip their Custom source.
func isInternalPromptSource(custom map[string]any) bool {
	if custom == nil {
		return false
	}
	switch custom["source"] {
	case "goal", "auto_verify":
		return true
	default:
		return false
	}
}

// executeBarrier runs a queued command at the idle point by translating the raw
// command line into the existing bus command(s), returning the command's error
// so the pump can classify transient (lost slot → retry) vs permanent (drop and
// announce) failures. Only PolicyQueue commands are ever enqueued as barriers,
// so this switch is exhaustive over that set; an unexpected command is a no-op.
func executeBarrier(sctx *SessionContext, raw string) error {
	name, rest := splitCommand(raw)
	switch name {
	case "compact":
		return sctx.Bus.Execute(CompactSession{SessionID: sctx.SessionID})
	case "prepare-compact":
		return sctx.Bus.Execute(PrepareCompactSession{SessionID: sctx.SessionID})
	case "clear":
		return sctx.Bus.Execute(ClearSession{SessionID: sctx.SessionID})
	case "model":
		spec := strings.TrimSpace(rest)
		if spec == "" {
			return nil // no argument: nothing to switch to (picker is a frontend concern)
		}
		return sctx.Bus.Execute(SwitchModel{SessionID: sctx.SessionID, ModelSpec: spec})
	case "thinking":
		level := strings.TrimSpace(rest)
		if level == "" {
			return nil
		}
		return sctx.Bus.Execute(SetThinking{SessionID: sctx.SessionID, Level: level})
	case "goal":
		return executeQueuedGoal(sctx, rest)
	case "verify":
		return sctx.Bus.Execute(RunManualVerify{SessionID: sctx.SessionID})
	}
	return nil
}

// executeQueuedGoal handles a queued "/goal <objective>" barrier. status/stop
// never queue (they are PolicyInstant), so a queued goal is always a start. A
// parse error is permanent: it is surfaced as a goal marker and returned so the
// barrier is dropped (not retried).
func executeQueuedGoal(sctx *SessionContext, rest string) error {
	gc, err := goal.ParseCommand(rest)
	if err != nil {
		appendGoalMarker(sctx, "🎯 Goal error: "+err.Error(), map[string]any{"error": true})
		return err
	}
	return sctx.Bus.Execute(EnterGoal{
		SessionID:     sctx.SessionID,
		Objective:     gc.Objective,
		CompactAt:     gc.CompactAt,
		VerifierSpec:  gc.VerifierSpec,
		MaxIterations: gc.MaxIterations,
		MaxStalled:    gc.MaxStalled,
		Timeout:       gc.Timeout,
		VerifyTimeout: gc.VerifyTimeout,
		VerifyOneShot: gc.VerifyOneShot,
		TotalBudget:   gc.TotalBudget,
		WorkDir:       gc.WorkDir,
	})
}
