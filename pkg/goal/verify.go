package goal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tool"
)

// DefaultVerifierSpec is the cheap, fast model used to judge the objective.
const DefaultVerifierSpec = "haiku"

// DefaultVerifyTimeout bounds a whole verifier run (wall-clock, across all its
// tool-using turns). Callers may override it (see VerifyConfig.Timeout); 0
// selects this default.
const DefaultVerifyTimeout = 5 * time.Minute

// Verifier guardrail defaults. The verifier is a read-only mini-agent: it reads
// the plan/state it's judging and checks a handful of requirements against the
// real repo, so it needs a few turns but must not run away.
const (
	defaultVerifierMaxTurns = 10
	// DefaultVerifierMaxBudget caps a single verifier run's spend (USD). Exported
	// so the driver can clamp it against the goal's remaining budget pool.
	DefaultVerifierMaxBudget = 0.50
)

// verifyMaxAttempts is how many times Verify retries a run that failed WITHOUT
// producing any assistant output (a transient stream/network blip). A run that
// produced output — even a bad verdict — is not retried.
const verifyMaxAttempts = 2

// oneShotMaxAttempts retries the legacy tool-less one-shot verifier. It's a
// cheap single call, so retrying transient failures costs cents.
const oneShotMaxAttempts = 3

// Verdict is the verifier's decision.
type Verdict struct {
	Satisfied bool   `json:"satisfied"`
	Feedback  string `json:"feedback"`
}

// VerifyStats reports what a verifier run consumed, so the driver can charge it
// against the goal budget.
type VerifyStats struct {
	CostUSD float64
	Usage   *core.Usage
	Turns   int
}

// ProviderFactory builds a provider for a given model. Callers pass the same
// factory they use elsewhere (it handles auth/OAuth refresh).
type ProviderFactory func(core.Model) (core.Provider, error)

// VerifyConfig configures a verifier run.
type VerifyConfig struct {
	Factory      ProviderFactory
	VerifierSpec string        // model spec; "" = DefaultVerifierSpec
	Objective    string        // the goal objective (verbatim /goal text)
	Evidence     string        // initial hint (diff + checks); NOT authoritative
	StatePath    string        // path to the goal's STATE.md (shown to the verifier)
	WorkDir      string        // read-only sandbox root for the verifier's tools
	Timeout      time.Duration // wall-clock TOTAL per run; 0 = DefaultVerifyTimeout
	MaxTurns     int           // 0 = defaultVerifierMaxTurns
	MaxBudget    float64       // 0 = DefaultVerifierMaxBudget
	OneShot      bool          // legacy tool-less one-shot mode
}

const verifierSystemPrompt = `You are a strict completion auditor in an autonomous coding loop. You did NOT write the work — your only job is to judge whether the stated OBJECTIVE has actually been met, and to report precisely what is missing if it has not.

You have READ-ONLY tools: read, grep, find, ls. USE THEM to check the real state of the repository. Follow this protocol:

1. If the OBJECTIVE references a plan or a document (or a GOAL STATE FILE is given), READ it first and derive a concrete list of the requirements / phases it demands.
2. Check each requirement against the ACTUAL state of the repo — read the files, search for the symbols or strings that would prove it's done. Judge the code, not the worker's claims.
3. The INITIAL EVIDENCE (a git diff, status, and build/test output) is only a hint to orient you. Verify anything doubtful or outside the diff yourself.

Be skeptical:
- A requirement that isn't demonstrably done is NOT satisfied — do not give the benefit of the doubt.
- If checks (build/tests) failed, or were "not run" when the objective needs them, it is NOT satisfied.
- A worker's self-report ("I did X, tests pass") is not evidence. Confirm it.

You have a limited turn budget. Be efficient: read only what you need, and do not re-read large files repeatedly.

When you have reached a verdict, STOP calling tools and reply with ONLY this JSON object — no prose, no markdown fences:
{"satisfied": <true|false>, "feedback": "<if not satisfied: list each unmet requirement concretely, naming the file or phase; if satisfied: a brief confirmation>"}`

// Verify judges whether the objective is satisfied. In the default (agentic)
// mode it runs a read-only mini-agent that can read the plan/state and check
// requirements against the real repo before returning a verdict. In OneShot
// mode it makes a single tool-less call (legacy behaviour).
//
// It returns the verdict, stats about what the run consumed (for budgeting),
// and an error only for genuine infrastructure failures — running out of
// turns/budget/time yields a not-satisfied verdict with feedback, NOT an error,
// so a healthy goal isn't paused just because the verifier was capped.
func Verify(ctx context.Context, cfg VerifyConfig) (Verdict, VerifyStats, error) {
	if cfg.Factory == nil {
		return Verdict{}, VerifyStats{}, fmt.Errorf("goal verify: nil provider factory")
	}
	spec := cfg.VerifierSpec
	if spec == "" {
		spec = DefaultVerifierSpec
	}
	model, ok := core.ResolveModel(spec)
	if !ok {
		return Verdict{}, VerifyStats{}, fmt.Errorf("goal verify: cannot resolve model %q", spec)
	}
	prov, err := cfg.Factory(model)
	if err != nil {
		return Verdict{}, VerifyStats{}, fmt.Errorf("goal verify: provider: %w", err)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultVerifyTimeout
	}

	if cfg.OneShot {
		verdict, stats, err := verifyOneShot(ctx, prov, model, cfg.Objective, cfg.Evidence, timeout)
		return verdict, stats, err
	}
	// The agentic verifier's tools are confined to WorkDir. An empty root would
	// make tool.safePath treat every path as allowed (YOLO), defeating the
	// read-only sandbox — refuse rather than expose the filesystem. Require an
	// existing directory too, so the verifier can't emit a verdict without ever
	// being able to inspect the workspace.
	if strings.TrimSpace(cfg.WorkDir) == "" {
		return Verdict{}, VerifyStats{}, fmt.Errorf("goal verify: WorkDir is required for the sandboxed verifier")
	}
	if info, err := os.Stat(cfg.WorkDir); err != nil || !info.IsDir() {
		return Verdict{}, VerifyStats{}, fmt.Errorf("goal verify: WorkDir %q is not an existing directory", cfg.WorkDir)
	}
	return verifyAgentic(ctx, prov, model, cfg, timeout)
}

// verifyAgentic runs the verifier as a read-only mini-agent.
func verifyAgentic(ctx context.Context, prov core.Provider, model core.Model, cfg VerifyConfig, timeout time.Duration) (Verdict, VerifyStats, error) {
	reg, err := newVerifierRegistry(cfg.WorkDir)
	if err != nil {
		return Verdict{}, VerifyStats{}, fmt.Errorf("goal verify: registry: %w", err)
	}

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultVerifierMaxTurns
	}
	// A budget can only be enforced when the model has pricing. A custom model
	// without pricing degrades to no $ cap (the turn/time caps still bound it).
	maxBudget := cfg.MaxBudget
	if maxBudget <= 0 {
		maxBudget = DefaultVerifierMaxBudget
	}
	if model.Pricing == nil {
		maxBudget = 0
	}

	// A single wall-clock deadline bounds the WHOLE verifier call — every retry
	// shares it, so the total never exceeds the configured timeout regardless of
	// how many attempts run. The per-agent MaxRunDuration is left at 0 (unlimited)
	// because this ctx is the authoritative bound.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	user := buildVerifierPrompt(cfg.Objective, cfg.StatePath, cfg.Evidence)

	// The budget pool is shared across retries: each attempt is capped at what's
	// left, so recreating the agent can't re-grant the full budget.
	remainingBudget := maxBudget
	// spentSoFar accumulates the real cost billed across every attempt so the
	// verdict we return charges the goal for all of it (including retried
	// attempts that produced no assistant message but still billed usage).
	var spentSoFar float64

	var lastErr error
	for attempt := 0; attempt < verifyMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			// The shared deadline expired between attempts. If we already produced
			// real output on an earlier attempt this wouldn't be reached; here it
			// means we ran out of time — a cap, not an infrastructure failure, so
			// the healthy goal keeps going rather than pausing.
			if errors.Is(err, context.DeadlineExceeded) {
				_, feedback := cappedRunFeedback(context.DeadlineExceeded)
				return Verdict{Satisfied: false, Feedback: feedback}, VerifyStats{CostUSD: spentSoFar}, nil
			}
			return Verdict{}, VerifyStats{CostUSD: spentSoFar}, fmt.Errorf("goal verify: %w", err)
		}

		child, err := agent.New(agent.AgentConfig{
			Provider:            prov,
			Model:               model,
			SystemPrompt:        verifierSystemPrompt,
			ThinkingLevel:       "low",
			Tools:               reg,
			WorkspaceRoot:       cfg.WorkDir,
			MaxTurns:            maxTurns,
			MaxToolCallsPerTurn: 10,
			MaxBudget:           remainingBudget,
			// Compaction disabled: a 10-turn read-only verifier won't exhaust the
			// context window, and a compaction call bills a summarization request
			// that wouldn't be reflected in the returned assistant messages —
			// i.e. cost the goal budget can't see. Keeping it off makes the
			// returned stats the true, complete cost of the run.
			Compaction: &core.CompactionSettings{Enabled: false},
		})
		if err != nil {
			return Verdict{}, VerifyStats{CostUSD: spentSoFar}, fmt.Errorf("goal verify: agent: %w", err)
		}

		msgs, runErr := child.Run(ctx, user)
		// child.RunCost() is the authoritative spend for this attempt (it counts
		// empty/failed-turn usage the loop billed but never surfaced as a
		// message). Accumulate it so the returned cost covers every attempt.
		spentSoFar += child.RunCost()
		remainingBudget = subtractBudget(remainingBudget, child.RunCost())
		stats := statsFrom(spentSoFar, msgs)

		// The parent context was cancelled (goal stopped / new run took over):
		// surface it so the driver bails instead of treating it as a verdict.
		if errors.Is(ctx.Err(), context.Canceled) {
			return Verdict{}, stats, fmt.Errorf("goal verify: %w", ctx.Err())
		}
		// Our own total deadline expired mid-run: treat as a cap (not-satisfied),
		// not an infrastructure failure, whether or not a turn completed.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			_, feedback := cappedRunFeedback(context.DeadlineExceeded)
			return Verdict{Satisfied: false, Feedback: feedback}, stats, nil
		}

		hadOutput := hadRealAssistantTurn(msgs)

		if runErr != nil {
			// Running out of turns/budget/time (or a doom loop) is not an
			// infrastructure failure: a capped verifier returns a not-satisfied
			// verdict with feedback so the healthy goal keeps going, rather than
			// pausing. A cap only counts if the verifier actually produced a real
			// turn — a Stream that failed before any assistant output is an
			// infrastructure failure (retried), even if it surfaces as a deadline.
			if hadOutput {
				if capped, feedback := cappedRunFeedback(runErr); capped {
					return Verdict{Satisfied: false, Feedback: feedback}, stats, nil
				}
			} else {
				// No real turn: transient/infrastructure failure — retry (within
				// the shared deadline).
				lastErr = fmt.Errorf("goal verify: run: %w", runErr)
				continue
			}
			// Output was produced despite the error — fall through and try to
			// parse a verdict from it.
		}

		out := core.ExtractFinalAssistantText(msgs)
		// Conservative fallback when the model didn't emit clean JSON:
		// not-satisfied, keeping whatever text it produced as a signal.
		// extractJSONObject already tolerates prose/fences around the object, so
		// a well-behaved verifier parses even without a dedicated reprompt (which
		// we avoid — a second Send would re-grant fresh turn/budget caps).
		return parseVerdict(out), stats, nil
	}
	return Verdict{}, VerifyStats{CostUSD: spentSoFar}, lastErr
}

// hadRealAssistantTurn reports whether a run produced a genuine assistant turn
// (one with recorded token usage). The agent appends a synthetic "(stopped: …)"
// assistant marker when a run fails before any tokens; that marker carries no
// Usage, so requiring Usage distinguishes a real (capped) run from an
// infrastructure failure that produced nothing.
func hadRealAssistantTurn(msgs []core.AgentMessage) bool {
	for _, m := range msgs {
		if m.Role == "assistant" && m.Usage != nil {
			return true
		}
	}
	return false
}

// subtractBudget lowers a remaining budget by spent, never below a tiny floor
// (a 0 or negative MaxBudget would mean "unlimited" to the agent, so we keep a
// positive sliver to preserve the cap semantics across retries).
func subtractBudget(remaining, spent float64) float64 {
	if remaining <= 0 {
		return remaining // 0 = unlimited (no pricing); leave as-is
	}
	r := remaining - spent
	if r < 0.0001 {
		return 0.0001
	}
	return r
}

// buildVerifierPrompt assembles the user message with the objective, the goal
// state file path, and the initial evidence hint.
func buildVerifierPrompt(objective, statePath, evidence string) string {
	var b strings.Builder
	b.WriteString("OBJECTIVE:\n")
	b.WriteString(strings.TrimSpace(objective))
	if strings.TrimSpace(statePath) != "" {
		b.WriteString("\n\nGOAL STATE FILE: ")
		b.WriteString(strings.TrimSpace(statePath))
	}
	b.WriteString("\n\nINITIAL EVIDENCE (a hint — verify it yourself):\n")
	b.WriteString(strings.TrimSpace(evidence))
	return b.String()
}

// cappedRunFeedback reports whether a run error is a guardrail cap (turns /
// budget / wall-clock) rather than an infrastructure failure, and returns
// feedback the maker can act on.
func cappedRunFeedback(err error) (bool, string) {
	if errors.Is(err, agent.ErrBudgetExceeded) {
		return true, "The verifier ran out of budget before reaching a verdict. Treating the objective as NOT yet satisfied. If the work looks complete, re-run; otherwise keep going."
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true, "The verifier ran out of time before reaching a verdict. Treating the objective as NOT yet satisfied. Continue the work."
	}
	if errors.Is(err, agent.ErrMaxTurnsExceeded) {
		return true, "The verifier ran out of inspection turns before reaching a verdict. Treating the objective as NOT yet satisfied. Continue the work."
	}
	if errors.Is(err, agent.ErrDoomLoop) {
		return true, "The verifier got stuck repeating the same inspection without reaching a verdict. Treating the objective as NOT yet satisfied. Continue the work."
	}
	return false, ""
}

// statsFrom sums usage across a run's assistant messages for reporting. The
// authoritative cost is passed in (child.RunCost()): it includes usage the loop
// billed for empty/failed turns that never surface as an assistant message, so
// re-deriving cost from msgs alone would undercount.
func statsFrom(runCost float64, msgs []core.AgentMessage) VerifyStats {
	var usage core.Usage
	var turns int
	found := false
	for _, m := range msgs {
		if m.Role == "assistant" && m.Usage != nil {
			found = true
			turns++
			usage.Input += m.Usage.Input
			usage.Output += m.Usage.Output
			usage.CacheRead += m.Usage.CacheRead
			usage.CacheWrite += m.Usage.CacheWrite
			usage.TotalTokens += m.Usage.TotalTokens
		}
	}
	stats := VerifyStats{CostUSD: runCost, Turns: turns}
	if found {
		stats.Usage = &usage
	}
	return stats
}

// newVerifierRegistry builds a read-only tool registry (read, grep, find, ls)
// sandboxed to workDir. It's a whitelist built from scratch — not a filtered
// copy of the session registry — so a newly-added tool can never leak in, and
// the tools resolve paths against the goal's own working directory.
//
// It builds its OWN restricted PathPolicy rooted at workDir rather than reusing
// the session policy: the session may be unrestricted (yolo) or carry extra
// allowed paths the maker was granted, and the verifier — which streams whatever
// it reads to a remote provider — must stay confined to the goal's worktree.
func newVerifierRegistry(workDir string) (*core.Registry, error) {
	// unrestricted=false, no extra allowed paths → containment to workDir only.
	pp := tool.NewPathPolicy(workDir, nil, false)
	cfg := tool.ToolConfig{WorkspaceRoot: workDir, PathPolicy: pp}
	reg := core.NewRegistry()
	if err := tool.RegisterRead(reg, cfg); err != nil {
		return nil, err
	}
	if err := tool.RegisterGrep(reg, cfg); err != nil {
		return nil, err
	}
	if err := tool.RegisterFind(reg, cfg); err != nil {
		return nil, err
	}
	if err := tool.RegisterLs(reg, cfg); err != nil {
		return nil, err
	}
	return reg, nil
}

// verifyOneShot is the legacy tool-less verifier: a single call with objective +
// evidence, no history and no tools. Kept for --verify-oneshot. It reports the
// usage/cost of the attempt that produced the verdict so the driver charges it
// against the goal budget, and shares one wall-clock deadline across retries.
func verifyOneShot(ctx context.Context, prov core.Provider, model core.Model, objective, evidence string, timeout time.Duration) (Verdict, VerifyStats, error) {
	user := fmt.Sprintf("OBJECTIVE:\n%s\n\nEVIDENCE:\n%s", strings.TrimSpace(objective), strings.TrimSpace(evidence))
	req := core.Request{
		Model:    model,
		System:   verifierSystemPrompt,
		Messages: []core.Message{core.NewUserMessage(user)},
		Options:  core.StreamOptions{ThinkingLevel: "off"},
	}

	// One deadline for the whole call, shared by every retry.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Accumulate real spend across attempts (a failed attempt can still bill
	// usage), so the returned cost covers every one — mirroring the agentic path.
	var spentSoFar float64
	// deadlineCap builds the conservative not-satisfied verdict for a total
	// timeout, carrying whatever we've spent so far.
	deadlineCap := func() (Verdict, VerifyStats, error) {
		_, feedback := cappedRunFeedback(context.DeadlineExceeded)
		return Verdict{Satisfied: false, Feedback: feedback}, VerifyStats{CostUSD: spentSoFar}, nil
	}

	var lastErr error
	for attempt := 0; attempt < oneShotMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			// Ran out of the shared wall-clock budget: a cap, not an
			// infrastructure failure — return a conservative not-satisfied verdict
			// so the healthy goal keeps going. A real cancellation stays an error.
			if errors.Is(err, context.DeadlineExceeded) {
				return deadlineCap()
			}
			return Verdict{}, VerifyStats{CostUSD: spentSoFar}, fmt.Errorf("goal verify: %w", err)
		}
		verdict, stats, err := oneShotAttempt(ctx, prov, model, req)
		spentSoFar += stats.CostUSD
		if err == nil {
			return verdict, VerifyStats{CostUSD: spentSoFar, Turns: stats.Turns, Usage: stats.Usage}, nil
		}
		lastErr = err
		// The attempt failed because the shared deadline expired: treat as a cap.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return deadlineCap()
		}
	}
	return Verdict{}, VerifyStats{CostUSD: spentSoFar}, lastErr
}

// oneShotAttempt runs a single tool-less verifier attempt and reports its
// usage/cost — including for an error path when the failing event still carries
// billable usage (e.g. an empty-response error), so callers can charge it.
func oneShotAttempt(ctx context.Context, prov core.Provider, model core.Model, req core.Request) (Verdict, VerifyStats, error) {
	ch, err := prov.Stream(ctx, req)
	if err != nil {
		return Verdict{}, VerifyStats{}, fmt.Errorf("goal verify: stream: %w", err)
	}

	var text strings.Builder
	var finalMsg *core.Message
	// Select on ctx.Done() so a provider that fails to close its channel after
	// cancellation can't outlive the shared deadline.
	for {
		select {
		case <-ctx.Done():
			return Verdict{}, VerifyStats{}, fmt.Errorf("goal verify: %w", ctx.Err())
		case event, ok := <-ch:
			if !ok {
				goto done
			}
			switch event.Type {
			case core.ProviderEventTextDelta:
				text.WriteString(event.Delta)
			case core.ProviderEventDone:
				finalMsg = event.Message
			case core.ProviderEventError:
				// A failed turn can still have billed usage (empty-response
				// error): surface its cost so the caller charges it.
				return Verdict{}, oneShotErrorStats(model, event.Error), fmt.Errorf("goal verify: %w", event.Error)
			}
		}
	}
done:

	out := text.String()
	if strings.TrimSpace(out) == "" && finalMsg != nil {
		for _, c := range finalMsg.Content {
			if c.Type == "text" {
				out += c.Text
			}
		}
	}

	var stats VerifyStats
	if finalMsg != nil && finalMsg.Usage != nil {
		stats.Turns = 1
		stats.Usage = finalMsg.Usage
		if model.Pricing != nil {
			stats.CostUSD = model.Pricing.Cost(*finalMsg.Usage)
		}
	}
	return parseVerdict(out), stats, nil
}

// oneShotErrorStats extracts billable usage from a failed-turn error (e.g. an
// empty-response error that still consumed input tokens) so the caller can
// charge it. Returns a zero VerifyStats when the error carries no usage.
func oneShotErrorStats(model core.Model, err error) VerifyStats {
	var emptyErr *core.EmptyResponseError
	if !errors.As(err, &emptyErr) || emptyErr.Usage == nil {
		return VerifyStats{}
	}
	stats := VerifyStats{Turns: 1, Usage: emptyErr.Usage}
	if model.Pricing != nil {
		stats.CostUSD = model.Pricing.Cost(*emptyErr.Usage)
	}
	return stats
}

// tryParseVerdict parses a strict verdict, reporting whether a JSON object was
// actually found and decoded (so callers can distinguish "clean verdict" from
// "no JSON, fall back").
func tryParseVerdict(s string) (Verdict, bool) {
	if raw := extractJSONObject(s); raw != "" {
		var v Verdict
		if err := json.Unmarshal([]byte(raw), &v); err == nil {
			return v, true
		}
	}
	return Verdict{}, false
}

// parseVerdict extracts the JSON verdict. On any parse failure it conservatively
// returns not-satisfied, keeping the raw text as feedback so the maker still
// gets a signal.
func parseVerdict(s string) Verdict {
	if v, ok := tryParseVerdict(s); ok {
		return v
	}
	return Verdict{Satisfied: false, Feedback: strings.TrimSpace(s)}
}

// extractJSONObject returns the substring from the first '{' to the last '}',
// tolerating markdown fences or surrounding prose. Returns "" if none found.
func extractJSONObject(s string) string {
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i < 0 || j < 0 || j < i {
		return ""
	}
	return s[i : j+1]
}
