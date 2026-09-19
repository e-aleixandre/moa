package serve

import (
	"log/slog"
	"sync/atomic"

	"github.com/e-aleixandre/moa/pkg/bus"
)

// runOutcome is what a run ended up as, seen from outside the session. It is
// the shared vocabulary of every consumer that reacts to a session finishing
// or getting stuck: the automation callback (a POST to the caller) and the
// project owner's reports both describe exactly these three outcomes.
type runOutcome struct {
	Status    string // done | failed | needs_input
	RunGen    uint64
	FinalText string
	Err       string
	Pending   *CallbackPending
}

// subscribeRunOutcomes installs the observer that turns bus traffic into
// runOutcome values for one sink. It was extracted from the automation
// callback, which is now one of its sinks, so both consumers share the same
// (carefully tuned) trigger policy:
//
//   - RunEnded with Err == nil → "done", but only once the session is fully
//     quiescent (no background subagent/bash work that could still push another
//     run), so we don't report completion in the middle of an autonomous chain.
//   - RunEnded with Err != nil → "failed", immediately: the run is over.
//   - PermissionRequested / AskUserRequested → "needs_input", carrying the
//     pending interaction, at most once per run (a run can ask many times; the
//     consumer only needs to learn that somebody has to answer). A later
//     RunEnded still delivers done/failed.
//
// All four triggers are handled by ONE SubscribeAll subscriber: it sees events
// in publication order on a single goroutine, which is what makes the
// once-per-run guard exact. Separate typed subscriptions would each run on
// their own goroutine, so a late RunStarted could clear the guard after a
// needs_input was already emitted (double fire), or a stale blocking event
// from the previous run could fire against the new run's reset guard.
//
// The sink always runs on its own goroutine: it must never block the bus, the
// run, or shutdown. Each sink gets its own subscription (and therefore its own
// guard), so one consumer can never suppress another's outcome.
func subscribeRunOutcomes(sess *ManagedSession, sink func(runOutcome)) {
	// lastRunGen records the most recent run we saw end, so a "done" waiter that
	// was still waiting for quiescence when a newer run started drops out: the
	// newer run reports its own outcome.
	var lastRunGen atomic.Uint64
	// needsInputSent is cleared at the start of every run, so a run that asks
	// five times still produces one needs_input outcome. Only ever touched from
	// the single subscriber goroutine below, in publication order.
	var needsInputSent bool

	needsInput := func(runGen uint64, pending *CallbackPending) {
		if needsInputSent {
			return
		}
		needsInputSent = true
		slog.Info("run outcome emitted", "codebase", sess.CWD, "session", sess.ID, "owner", "", "status", callbackStatusNeedsInput, "run_gen", runGen, "batch", "", "n", 1)
		go sink(runOutcome{Status: callbackStatusNeedsInput, RunGen: runGen, Pending: pending})
	}

	sess.pushUnsubs = append(sess.pushUnsubs, sess.runtime.Bus.SubscribeAll(func(event any) {
		switch e := event.(type) {
		case bus.RunStarted:
			needsInputSent = false
		case bus.PermissionRequested:
			needsInput(e.RunGen, permissionPending(e))
		case bus.AskUserRequested:
			needsInput(e.RunGen, askPending(e))
		case bus.RunEnded:
			lastRunGen.Store(e.RunGen)
			if e.Err != nil {
				slog.Info("run outcome emitted", "codebase", sess.CWD, "session", sess.ID, "owner", "", "status", callbackStatusFailed, "run_gen", e.RunGen, "batch", "", "n", 1)
				go sink(runOutcome{
					Status:    callbackStatusFailed,
					RunGen:    e.RunGen,
					FinalText: e.FinalText,
					Err:       e.Err.Error(),
				})
				return
			}
			go func() {
				// WaitQuiescent drains the bus, so it must not run on a
				// subscriber goroutine (it would wait on itself).
				if !sess.runtime.WaitQuiescent(sess.infra.sessionCtx) {
					slog.Info("run outcome discarded", "codebase", sess.CWD, "session", sess.ID, "owner", "", "status", callbackStatusDone, "run_gen", e.RunGen, "batch", "", "n", 0, "reason", "session going away")
					return // session is going away; nothing useful to report
				}
				if lastRunGen.Load() != e.RunGen {
					slog.Info("run outcome discarded", "codebase", sess.CWD, "session", sess.ID, "owner", "", "status", callbackStatusDone, "run_gen", e.RunGen, "batch", "", "n", 0, "reason", "superseded")
					return // superseded by a newer run, which reports for itself
				}
				slog.Info("run outcome emitted", "codebase", sess.CWD, "session", sess.ID, "owner", "", "status", callbackStatusDone, "run_gen", e.RunGen, "batch", "", "n", 1)
				sink(runOutcome{Status: callbackStatusDone, RunGen: e.RunGen, FinalText: e.FinalText})
			}()
		}
	}))
}
