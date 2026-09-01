// handlers_verify.go contains bus handlers for the corresponding session concerns.

package bus

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/e-aleixandre/moa/pkg/verify"
)

func registerManualVerifyHandler(sctx *SessionContext) {
	b := sctx.Bus
	// Serializes manual /verify runs (bus command and queued barrier share it).
	var manualVerifyRunning atomic.Bool
	b.OnCommand(func(cmd RunManualVerify) error {
		if err := RequireManualVerifyAllowed(sctx.Bus); err != nil {
			return err
		}
		// Occupy the session state for the whole verify (idle → running → idle),
		// like a run: this is what keeps a queued /verify barrier in its position
		// (a concurrent SendPrompt can't slip in while it runs). A second caller
		// fails the transition and gets ErrSessionBusy. The atomic below is a
		// belt against two verifies that both somehow saw idle. The serve
		// direct /verify command does not go through here; only the queued
		// barrier uses this state-occupying path.
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

		cwd, err := verify.ResolveWorkDir(sctx.CWD, cmd.Dir, sctx.PathPolicy)
		if err != nil {
			sctx.Bus.Publish(AutoVerifyEnded{SessionID: sctx.SessionID, Err: err})
			return err
		}
		dir := ""
		if cwd != sctx.CWD {
			dir = cwd
		}
		sctx.Bus.Publish(AutoVerifyStarted{SessionID: sctx.SessionID, Dir: dir, Manual: true})
		ctx, cancel := context.WithTimeout(sctx.SessionCtx, 5*time.Minute)
		defer cancel()

		result, err := verify.Execute(ctx, cwd)
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
}

func registerAutoVerifyReactor(sctx *SessionContext, shared *handlerSharedState, launchAutoVerify func(func())) {
	b := sctx.Bus
	autoVerifyCount := &shared.autoVerifyCount
	autoVerifyCancel := &shared.autoVerifyCancel
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

		ctx, cancel := context.WithTimeout(sctx.SessionCtx, 5*time.Minute)
		// Register before launching so a new user turn cannot miss the cancel
		// function while this goroutine is waiting to be scheduled.
		autoVerifyCancel.Store(&cancel)

		launchAutoVerify(func() {
			defer sctx.endAutoVerify()
			defer cancel()
			defer autoVerifyCancel.CompareAndSwap(&cancel, nil)
			sctx.Bus.Publish(AutoVerifyStarted{SessionID: sctx.SessionID})

			// Do not touch the workspace if cancellation or a newer run won before
			// this goroutine reached the verifier.
			if ctx.Err() != nil || sctx.RunGenAtomic.Load() != startRunGen {
				return
			}

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
		})
	})

}

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
