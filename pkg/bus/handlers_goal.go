// handlers_goal.go contains bus handlers for the corresponding session concerns.

package bus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/e-aleixandre/moa/pkg/core"
	"github.com/e-aleixandre/moa/pkg/goal"
	"github.com/e-aleixandre/moa/pkg/verify"
)

func registerGoalHandlers(sctx *SessionContext) {
	b := sctx.Bus
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
}

func registerGoalReactor(sctx *SessionContext) {
	b := sctx.Bus
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
			// total too (the web usage widget), not only the goal
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
				_ = sctx.Agent.SetCompactAt(prev)
				_ = sctx.Agent.SetMaxBudget(prevBudget)
				// Re-apply now that the goal directive is gone. Runs at the end
				// of a goal, where the agent is not running; nothing here could
				// act on a refusal anyway.
				_ = rebuildSystemPrompt(sctx)
			}
			mu.Lock()
			tearDown()
			mu.Unlock()
		}
		u := sctx.Bus.Subscribe(handler)
		mu.Lock()
		unsub = u
		// RunEnded may have fired before Subscribe returned; complete teardown
		// here so the one-shot subscription cannot leak or call a nil function.
		tearDown()
		mu.Unlock()
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
