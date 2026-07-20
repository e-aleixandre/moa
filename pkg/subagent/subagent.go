package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/agent"
	"github.com/ealeixandre/moa/pkg/bus"
	agentcontext "github.com/ealeixandre/moa/pkg/context"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tool"
)

const (
	jobTTL = 30 * time.Minute

	// asyncResultTailLines is how many trailing lines of a completed async
	// subagent result are included in the notification to the parent.
	asyncResultTailLines = 50

	// defaultChildMaxTurns is the fallback per-child turn limit used when
	// Config.ChildMaxTurns is not set. Independent of the parent's own
	// MaxTurns. Was 30, which was too tight for non-trivial delegated tasks
	// (exploration + multi-file edits + build/test verification routinely
	// exceeded it, killing the subagent mid-task with no partial result).
	defaultChildMaxTurns = 100

	// defaultChildMaxRunDuration is the fallback per-child wall-clock budget
	// used when Config.ChildMaxRunDuration is not set.
	defaultChildMaxRunDuration = 10 * time.Minute

	// defaultMaxConcurrentAsync is the fallback cap on simultaneously running
	// async subagent jobs, used when Config.MaxConcurrentAsync is not set.
	defaultMaxConcurrentAsync = 5
)

// excludedTools prevents recursive subagent spawning.
// Subagents are leaf workers, not orchestrators.
var excludedTools = map[string]bool{
	"checkpoint":      true,
	"subagent":        true,
	"subagent_status": true,
	"subagent_wait":   true,
	"subagent_cancel": true,
	"memory":          true,
	"ask_user":        true,
}

type Config struct {
	DefaultModel           core.Model
	CurrentModel           func() core.Model
	CurrentThinkingLevel   func() string
	CurrentPermissionCheck func() func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision
	ProviderFactory        func(core.Model) (core.Provider, error)
	AgentsMD               string
	PromptBuilder          func(opts agentcontext.SystemPromptOptions) string
	ParentTools            *core.Registry
	AppCtx                 context.Context
	WorkspaceRoot          string // CWD passed to system prompt builder
	SkillsIndex            string // pre-formatted skills index for system prompt
	MemoryIndex            string // pre-formatted memory index (one line per fact)

	// BashState, when non-nil, is the per-agent persistent shell state. The
	// subagent seeds an isolated copy for the child (subshell semantics) and
	// drops it when the child finishes. nil = no shell-state isolation.
	BashState *tool.BashState

	// OnAsyncComplete is called when an async subagent finishes (completed, failed, or cancelled).
	// truncated is true when resultTail is only the last N lines of the full output.
	OnAsyncComplete func(jobID, task, status, resultTail string, truncated bool)

	// OnAsyncJobChange is called when an async job starts or finishes.
	// count is the current number of running jobs.
	OnAsyncJobChange func(count int)

	// OnChildStart is called right before a child agent (sync or async) begins
	// running, with its jobID/task/model, whether it's async, its start
	// time (so live UIs can compute elapsed and reconcile it after a reconnect),
	// and its stable per-session creation ordinal (accentIndex) for a
	// deterministic accent color that survives reconnects.
	OnChildStart func(jobID, task, model string, async bool, startedAt time.Time, accentIndex int)

	// OnChildEvent is called for each typed bus event produced by translating
	// the child's core.AgentEvent stream (via bus.TranslateAgentEvent). inner
	// is already a concrete bus.* type (e.g. bus.TextDelta), never a raw
	// core.AgentEvent — pkg/subagent imports pkg/bus directly (no import
	// cycle: pkg/bus does not import pkg/subagent), so translation happens
	// here rather than at the call site.
	OnChildEvent func(jobID string, inner any)

	// OnChildUsage is called each time a child closes a message (its
	// message_end), with the child's accumulated usage/cost so far (cost using
	// the CHILD's model pricing). It lets live UIs show running tokens/cost
	// before the terminal OnChildEnd. Same aggregation as OnChildEnd, so the
	// live value stays consistent with the final total.
	OnChildUsage func(jobID string, usage *core.Usage, costUSD float64)

	// OnChildEnd is called once when a child agent (sync or async) finishes,
	// with its final status and aggregated usage/cost (cost computed with the
	// CHILD's model pricing, which may differ from the parent's).
	OnChildEnd func(jobID, status string, usage *core.Usage, costUSD float64)

	// ChildMaxTurns caps the number of turns a child agent may take. 0 (or
	// negative) falls back to defaultChildMaxTurns.
	ChildMaxTurns int

	// ChildMaxRunDuration caps how long a child agent may run. 0 (or
	// negative) falls back to defaultChildMaxRunDuration.
	ChildMaxRunDuration time.Duration

	// MaxConcurrentAsync caps how many async subagent jobs may run at once.
	// 0 (or negative) falls back to defaultMaxConcurrentAsync.
	MaxConcurrentAsync int

	// TranscriptLoader loads a persisted subagent transcript's messages by job
	// ID, enabling the "resume" parameter to continue a finished subagent's
	// conversation instead of starting fresh. nil = resume unsupported (the
	// tool reports a clear error when resume is requested). The caller wires
	// this to its session-scoped transcript store (see pkg/serve).
	TranscriptLoader func(jobID string) ([]core.AgentMessage, error)
}

// RegisterAll registers the subagent tools on reg and returns a handle onto
// the job store (for external consumers: init snapshot, tray UI, cancellation).
func RegisterAll(reg *core.Registry, cfg Config) (*Jobs, error) {
	if cfg.AppCtx == nil {
		return nil, errors.New("subagent: AppCtx is required")
	}
	jobs := newJobStore()
	for _, t := range []core.Tool{
		newSubagent(cfg, jobs),
		newSubagentStatus(jobs),
		newSubagentWait(jobs),
		newSubagentCancel(jobs),
	} {
		if err := reg.Register(t); err != nil {
			return nil, fmt.Errorf("subagent: %w", err)
		}
	}
	return &Jobs{store: jobs}, nil
}

func newSubagent(cfg Config, jobs *jobStore) core.Tool {
	return core.Tool{
		Name:        "subagent",
		Label:       "Subagent",
		Description: "Spawn a child agent with its own context for focused subtasks or background work.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"task": {
					"type": "string",
					"description": "The task for the subagent to complete"
				},
				"tools": {
					"type": "array",
					"items": { "type": "string" },
					"description": "Tool names the subagent can use. Omit for all available tools."
				},
				"model": {
					"type": "string",
					"description": "Model to use. Defaults to the configured model."
				},
				"thinking": {
					"type": "string",
					"description": "Thinking level for the subagent: off, low, medium, high, xhigh. Defaults to the current parent thinking level."
				},
				"max_duration": {
					"type": "string",
					"description": "Max wall-clock time for the subagent as a Go duration (e.g. \"20m\", \"1h\"). Omit for the default (10m). Raise it for long tasks so the child is not killed mid-work."
				},
				"resume": {
					"type": "string",
					"description": "Job ID of a previous subagent to resume: its saved transcript is reloaded and 'task' is sent as the next message, continuing that conversation instead of starting fresh."
				},
				"async": {
					"type": "boolean",
					"description": "Run in background and return a job ID. Use subagent_wait to block until it finishes, subagent_status to peek at progress, and subagent_cancel to stop."
				}
			},
			"required": ["task"]
		}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			jobs.cleanup(jobTTL)
			if err := ctx.Err(); err != nil {
				return core.ErrorResult(err.Error()), nil
			}

			task, _ := params["task"].(string)
			if strings.TrimSpace(task) == "" {
				return core.ErrorResult("task is required"), nil
			}

			childReg, errResult := buildChildRegistry(cfg.ParentTools, params)
			if errResult != nil {
				return *errResult, nil
			}

			model, errResult := resolveModel(currentModel(cfg), params)
			if errResult != nil {
				return *errResult, nil
			}
			thinkingLevel, errResult := resolveThinking(currentThinkingLevel(cfg), params)
			if errResult != nil {
				return *errResult, nil
			}
			maxRunDuration, errResult := resolveMaxDuration(params)
			if errResult != nil {
				return *errResult, nil
			}
			seedMsgs, errResult := resolveResume(cfg, params)
			if errResult != nil {
				return *errResult, nil
			}
			if cfg.ProviderFactory == nil {
				return core.ErrorResult("subagent provider factory is not configured"), nil
			}
			provider, err := cfg.ProviderFactory(model)
			if err != nil {
				return core.ErrorResult(err.Error()), nil
			}

			promptBuilder := cfg.PromptBuilder
			if promptBuilder == nil {
				promptBuilder = agentcontext.BuildSystemPrompt
			}
			systemPrompt := buildSystemPrompt(promptBuilder, cfg.AgentsMD, childReg.Specs(), cfg.WorkspaceRoot, cfg.SkillsIndex, cfg.MemoryIndex)

			if getBool(params, "async") {
				if err := ctx.Err(); err != nil {
					return core.ErrorResult(err.Error()), nil
				}
				maxConcurrent := cfg.MaxConcurrentAsync
				if maxConcurrent <= 0 {
					maxConcurrent = defaultMaxConcurrentAsync
				}
				if running := jobs.runningCount(); running >= maxConcurrent {
					return core.ErrorResult(fmt.Sprintf("too many concurrent async subagents (%d running, max %d); wait or cancel one with subagent_cancel", running, maxConcurrent)), nil
				}
				jobCtx, jobCancel := context.WithCancel(cfg.AppCtx)
				job := jobs.create(task, model.ID, jobCancel)
				// Isolate the child's shell state: seed a copy from the parent
				// (read from the spawning ctx) and tag the child ctx with its
				// job ID. runJob drops the snapshot when it finishes.
				if cfg.BashState != nil {
					cfg.BashState.Seed(job.id, core.AgentIDFromContext(ctx))
					jobCtx = core.WithAgentID(jobCtx, job.id)
				}
				if cfg.OnAsyncJobChange != nil {
					cfg.OnAsyncJobChange(jobs.runningCount())
				}
				go runJob(jobCtx, cfg, jobs, job, provider, model, thinkingLevel, maxRunDuration, systemPrompt, childReg, task, seedMsgs, nil)
				return core.TextResult("Subagent started in background.\nJob ID: " + job.id + "\nUse subagent_wait to block until it finishes, subagent_status to peek at progress, or subagent_cancel to stop. You'll also be notified when it completes."), nil
			}

			// Sync: the child still runs in its own goroutine (unified with
			// async via runJob), so it can survive being promoted to
			// background. The parent blocks in awaitSyncResult until the
			// child finishes or is promoted. jobCtx derives from cfg.AppCtx
			// (like async) rather than from ctx, so a promoted child is not
			// tied to the parent tool call's context; a linker goroutine
			// propagates the parent's cancellation into the child only while
			// it has not been promoted.
			jobCtx, jobCancel := context.WithCancel(cfg.AppCtx)
			job := jobs.createSync(task, model.ID, jobCancel)
			// Isolate the child's shell state (subshell semantics): seed a
			// copy from the parent and tag the child ctx with the job's own
			// ID (stable across a promotion, unlike the old ephemeral
			// childID). runJob drops the snapshot when it finishes.
			if cfg.BashState != nil {
				cfg.BashState.Seed(job.id, core.AgentIDFromContext(ctx))
				jobCtx = core.WithAgentID(jobCtx, job.id)
			}
			go linker(ctx, jobCancel, job)
			go runJob(jobCtx, cfg, jobs, job, provider, model, thinkingLevel, maxRunDuration, systemPrompt, childReg, task, seedMsgs, onUpdate)
			return awaitSyncResult(cfg, jobs, job, task, model)
		},
	}
}

func newSubagentStatus(jobs *jobStore) core.Tool {
	return core.Tool{
		Name:        "subagent_status",
		Label:       "Subagent Status",
		Description: "Check the status of an async subagent job.",
		Effect:      core.EffectReadOnly,
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"job_id": {
					"type": "string",
					"description": "The job ID returned by an async subagent call"
				}
			},
			"required": ["job_id"]
		}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			jobs.cleanup(jobTTL)
			jobID, _ := params["job_id"].(string)
			snap, ok := jobs.snapshot(jobID)
			if !ok {
				return core.ErrorResult("unknown job ID: " + jobID), nil
			}
			return core.TextResult(formatStatus(snap)), nil
		},
	}
}

// subagentWaitMaxTimeout caps an explicit timeout so a huge value can't
// overflow time.Duration into a negative "wait forever" sentinel.
const subagentWaitMaxTimeout = 24 * time.Hour

func newSubagentWait(jobs *jobStore) core.Tool {
	return core.Tool{
		Name:        "subagent_wait",
		Label:       "Subagent Wait",
		Description: "Wait for an async subagent job to finish and return its result. Use this instead of polling subagent_status when you need the result to continue.",
		Effect:      core.EffectReadOnly,
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"job_id": {
					"type": "string",
					"description": "The job ID returned by an async subagent call"
				},
				"timeout": {
					"type": "integer",
					"description": "Max seconds to wait (default 600). On timeout returns the current status without failing."
				}
			},
			"required": ["job_id"]
		}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			jobs.cleanup(jobTTL)
			jobID, _ := params["job_id"].(string)
			timeout := subagentWaitTimeout(params)
			snap, delivered, err := jobs.wait(ctx, jobID, timeout)
			if err != nil {
				if errors.Is(err, ErrUnknownJob) {
					return core.ErrorResult("unknown job ID: " + jobID), nil
				}
				return core.ErrorResult("subagent_wait cancelled"), nil
			}
			if snap.Status == statusRunning || snap.Status == statusCancelling {
				return core.TextResult(formatStatus(snap) + "\n\n(still running after timeout; call subagent_wait again to keep waiting, or subagent_cancel to stop it)"), nil
			}
			if !delivered {
				// The async completion notification already delivered this
				// subagent's full result to the conversation; don't repeat it.
				return core.TextResult(fmt.Sprintf("Subagent %s already finished (status: %s); its result was delivered above.", snap.ID, snap.Status)), nil
			}
			return core.TextResult(formatStatus(snap)), nil
		},
	}
}

func subagentWaitTimeout(params map[string]any) time.Duration {
	secs := 600
	switch v := params["timeout"].(type) {
	case float64:
		secs = int(v)
	case int:
		secs = v
	}
	if secs <= 0 {
		return 0
	}
	if time.Duration(secs) > subagentWaitMaxTimeout/time.Second {
		return subagentWaitMaxTimeout
	}
	return time.Duration(secs) * time.Second
}

func newSubagentCancel(jobs *jobStore) core.Tool {
	return core.Tool{
		Name:        "subagent_cancel",
		Label:       "Subagent Cancel",
		Description: "Cancel a running async subagent job.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"job_id": {
					"type": "string",
					"description": "The job ID returned by an async subagent call"
				}
			},
			"required": ["job_id"]
		}`),
		Execute: func(ctx context.Context, params map[string]any, onUpdate func(core.Result)) (core.Result, error) {
			jobs.cleanup(jobTTL)
			jobID, _ := params["job_id"].(string)
			j, snap, requested := jobs.requestCancel(jobID)
			if j == nil {
				return core.ErrorResult("unknown job ID: " + jobID), nil
			}
			if !requested {
				return core.TextResult("Job already " + snap.Status), nil
			}

			j.cancel()

			select {
			case <-j.done:
				final, ok := jobs.snapshot(jobID)
				if !ok {
					return core.TextResult("Job cancelled"), nil
				}
				switch final.Status {
				case statusCancelled:
					return core.TextResult("Job cancelled"), nil
				case statusCompleted:
					return core.TextResult("Job already completed"), nil
				case statusFailed:
					return core.TextResult("Job failed before cancellation completed"), nil
				default:
					return core.TextResult("Cancellation requested"), nil
				}
			case <-time.After(5 * time.Second):
				return core.TextResult("Cancellation requested"), nil
			case <-ctx.Done():
				return core.ErrorResult(ctx.Err().Error()), nil
			}
		},
	}
}

// linker propagates parentCtx's cancellation into jobCancel, but only while
// the job has not been promoted (or already finished). Once promoted, the
// child is deliberately decoupled from the parent's context so it survives
// the parent tool call returning.
//
// The re-check after parentCtx.Done() is essential: when the parent tool call
// returns right after a promotion, its ctx is cancelled AND j.promoted is
// closed at nearly the same time. A plain select would pick a ready case
// pseudo-randomly and could call jobCancel() on an already-promoted (or
// already-finished) child, killing it. So on parentCtx.Done() we cancel only
// if the job is still neither promoted nor done.
func linker(parentCtx context.Context, jobCancel context.CancelFunc, j *job) {
	select {
	case <-j.promoted:
		return
	case <-j.done:
		return
	case <-parentCtx.Done():
		if j.isPromoted() {
			return
		}
		select {
		case <-j.done:
			return
		default:
			jobCancel()
		}
	}
}

// awaitSyncResult blocks until j finishes or is promoted to background,
// deciding which happened by consulting j.isPromoted() — never by which
// channel of the select fired, since Go's select picks pseudo-randomly among
// ready cases and both may be ready in a promote-vs-finish race. This
// guarantees the result is delivered exactly once, via a single lane:
// promoted → async (OnAsyncComplete, from runJob's defer); not promoted →
// this function's return value.
func awaitSyncResult(cfg Config, jobs *jobStore, j *job, task string, model core.Model) (core.Result, error) {
	select {
	case <-j.done:
	case <-j.promoted:
	}

	if j.isPromoted() {
		if cfg.OnChildStart != nil {
			var startedAt time.Time
			var accentIndex int
			if snap, ok := jobs.snapshot(j.id); ok {
				startedAt = snap.StartedAt
				accentIndex = snap.AccentIndex
			}
			cfg.OnChildStart(j.id, task, model.ID, true, startedAt, accentIndex)
		}
		if cfg.OnAsyncJobChange != nil {
			cfg.OnAsyncJobChange(jobs.runningCount())
		}
		return core.TextResult("Subagent promoted to background.\nJob ID: " + j.id + "\nYou'll be notified when it finishes; use subagent_wait to block on it or subagent_status to peek at progress."), nil
	}

	snap, ok := jobs.snapshot(j.id)
	jobs.delete(j.id)
	if !ok {
		return core.ErrorResult("subagent job disappeared"), nil
	}
	switch snap.Status {
	case statusCompleted:
		return core.TextResult(snap.Result), nil
	case statusFailed:
		return core.ErrorResult(snap.Error), nil
	case statusCancelled:
		return core.ErrorResult("cancelled"), nil
	default:
		return core.ErrorResult("subagent finished in unexpected state: " + snap.Status), nil
	}
}

// runJob runs a child agent to completion, tracking its progress/result on
// jobs and notifying cfg's callbacks. Used for both async jobs (onUpdate ==
// nil) and sync jobs (onUpdate delivers streamed output back to the parent's
// blocking tool call) — the job's isSync() flag (checked per-event, since a
// sync job may be promoted to async mid-run) decides how each streamed event
// is forwarded, so there is a single subscription with no resubscription and
// therefore no risk of losing or duplicating events across a promotion.
func runJob(jobCtx context.Context, cfg Config, jobs *jobStore, j *job, provider core.Provider, model core.Model, thinkingLevel string, maxRunDuration time.Duration, systemPrompt string, childReg *core.Registry, task string, seedMsgs []core.AgentMessage, onUpdate func(core.Result)) {
	defer j.cancel()
	defer close(j.done)
	var finalMsgs []core.AgentMessage
	if cfg.BashState != nil {
		defer cfg.BashState.Drop(j.id)
	}
	defer func() {
		// Only notify the async lanes if the job is (now) async — either it
		// started async, or it was promoted while running. A sync job that
		// finished without being promoted delivers its result via the
		// parent's return value in awaitSyncResult instead, and must not
		// also fire these callbacks (single delivery lane).
		if !j.isSync() {
			// The terminal transition selected the waiter-vs-completion owner
			// under j.mu; consume that async claim before close(j.done).
			// UI/count callbacks still fire below when a waiter owns the result.
			if j.claimAsyncCompletion() && cfg.OnAsyncComplete != nil {
				snap, ok := jobs.snapshot(j.id)
				if ok {
					// A failed job carries its message in Error, not Result, so
					// deliver whichever is populated — otherwise a failure
					// (e.g. a timeout, with its actionable text + partial) would
					// surface as an empty notification.
					payload := snap.Result
					if snap.Status == statusFailed {
						payload = snap.Error
					}
					tail, wasTruncated := tailLinesWithFlag(payload, asyncResultTailLines)
					cfg.OnAsyncComplete(snap.ID, snap.Task, snap.Status, tail, wasTruncated)
				}
			}
			if cfg.OnAsyncJobChange != nil {
				cfg.OnAsyncJobChange(jobs.runningCount())
			}
		}
		if cfg.OnChildEnd != nil {
			snap, ok := jobs.snapshot(j.id)
			status := statusFailed
			if ok {
				status = snap.Status
			}
			cfg.OnChildEnd(j.id, status, childUsage(finalMsgs), childCost(model, finalMsgs))
		}
	}()

	child, err := newChildAgent(cfg, provider, model, thinkingLevel, maxRunDuration, systemPrompt, childReg)
	if err != nil {
		jobs.setFailed(j.id, err.Error())
		return
	}
	jobs.setChildAgent(j.id, child)
	unsub := child.Subscribe(func(e core.AgentEvent) {
		if j.isSync() {
			forwardSyncEvent(e, onUpdate)
		} else {
			forwardAsyncEvent(jobs, j.id, e)
		}
		forwardChildEvent(cfg, j.id, e)
		if e.Type == core.AgentEventMessageEnd {
			msgs := child.Messages()
			jobs.setMessages(j.id, msgs)
			// Record and emit running usage/cost so live UIs can show
			// accumulated tokens/cost before the terminal OnChildEnd. Same
			// aggregation as OnChildEnd, keeping the live value consistent
			// with the final total.
			usage, cost := childUsage(msgs), childCost(model, msgs)
			jobs.setUsage(j.id, usage, cost)
			if cfg.OnChildUsage != nil {
				cfg.OnChildUsage(j.id, usage, cost)
			}
		}
	})
	defer unsub()

	if cfg.OnChildStart != nil {
		var startedAt time.Time
		var accentIndex int
		if snap, ok := jobs.snapshot(j.id); ok {
			startedAt = snap.StartedAt
			accentIndex = snap.AccentIndex
		}
		cfg.OnChildStart(j.id, task, model.ID, !j.isSync(), startedAt, accentIndex)
	}

	msgs, err := runChild(jobCtx, child, task, seedMsgs)
	finalMsgs = msgs
	jobs.setMessages(j.id, msgs)
	jobs.setUsage(j.id, childUsage(msgs), childCost(model, msgs))
	if err != nil {
		// Classify from authoritative signals, not the returned error's chain
		// (a provider may wrap a context error while the context is still live).
		// jobCtx.Err() is the authoritative "was THIS job cancelled" signal —
		// subagent_cancel and an AppCtx shutdown both cancel jobCtx. Check it
		// first so a genuine cancel wins even if a deadline also tripped in the
		// same unwind.
		if jobCtx.Err() == context.Canceled {
			jobs.setCancelled(j.id)
			return
		}
		if child.TimedOut() {
			// The child exhausted its own MaxRunDuration budget (not an
			// inherited parent deadline — child.TimedOut() already excludes
			// that). Surface an actionable message instead of the cryptic
			// "stream: context deadline exceeded", and keep whatever it produced
			// before the deadline so the work isn't lost.
			_, effective := resolveChildGuardrails(cfg, maxRunDuration)
			jobs.setFailed(j.id, timeoutMessage(effective, timeoutPartial(msgs)))
			return
		}
		jobs.setFailed(j.id, err.Error())
		return
	}
	jobs.setCompleted(j.id, core.ExtractFinalAssistantText(msgs))
}

// timeoutMessage builds the actionable text shown when a subagent exhausts its
// wall-clock budget: any real partial output the child produced, followed by the
// effective duration that tripped and a suggested larger max_duration. The
// actionable guidance goes LAST so it survives tail-truncation on the async
// notification path (which keeps the final lines).
func timeoutMessage(effective time.Duration, partial string) string {
	guidance := fmt.Sprintf("subagent timed out after %s (its max run duration). Re-run with a larger max_duration (e.g. %q) for long tasks, or split the task into smaller steps.", effective, formatDurationArg(suggestLongerDuration(effective)))
	if partial != "" {
		return "Partial output before the timeout:\n" + partial + "\n\n" + guidance
	}
	return guidance
}

// timeoutPartial returns the child's real partial assistant text, excluding the
// synthetic "(run timed out)" marker the agent inserts when the deadline trips
// before any reply — that marker is a status note, not model output.
func timeoutPartial(msgs []core.AgentMessage) string {
	partial := core.ExtractFinalAssistantText(msgs)
	if partial == agent.MarkerRunTimedOut {
		return ""
	}
	return partial
}

// formatDurationArg renders a whole-minute duration the way the max_duration
// arg expects (a Go duration string): "20m", "1h", "1h30m" rather than Go's
// default "20m0s" / "1h0m0s". Assumes d is a whole number of minutes (what
// suggestLongerDuration produces).
func formatDurationArg(d time.Duration) string {
	mins := int(d / time.Minute)
	h, m := mins/60, mins%60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh%dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// suggestLongerDuration proposes a bigger max_duration to retry a timed-out
// subagent with: double the exhausted budget, rounded UP to a whole minute so
// the suggestion is never smaller than the real double, with a 1m floor. Guards
// against overflow for absurdly large budgets by capping instead of wrapping.
func suggestLongerDuration(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Minute
	}
	// Double the budget, but never wrap: cap at MaxInt64 for astronomical inputs.
	doubled := time.Duration(math.MaxInt64)
	if d <= math.MaxInt64/2 {
		doubled = d * 2
	}
	return roundUpToMinute(doubled)
}

// roundUpToMinute rounds d up to the next whole minute, with a 1m floor, without
// overflowing: if rounding up would wrap past MaxInt64, it rounds DOWN to the
// current whole minute instead (still positive and a valid Go duration).
func roundUpToMinute(d time.Duration) time.Duration {
	if d < time.Minute {
		return time.Minute
	}
	r := d % time.Minute
	if r == 0 {
		return d
	}
	if d > math.MaxInt64-(time.Minute-r) {
		return d - r // rounding up would overflow; round down to a whole minute
	}
	return d + (time.Minute - r)
}

// forwardChildEvent translates a child's core.AgentEvent into typed bus
// event(s) (via bus.TranslateAgentEvent, the same translation used for the
// main session) and forwards each to cfg.OnChildEvent, namespaced by jobID.
// taskStore is always nil here: subagent children have no task list of their
// own to diff against.
func forwardChildEvent(cfg Config, jobID string, e core.AgentEvent) {
	if cfg.OnChildEvent == nil {
		return
	}
	for _, inner := range bus.TranslateAgentEvent(jobID, 0, e, nil) {
		cfg.OnChildEvent(jobID, inner)
	}
}

// childUsage sums Usage across a child's assistant messages.
func childUsage(msgs []core.AgentMessage) *core.Usage {
	var total core.Usage
	found := false
	for _, m := range msgs {
		if m.Role == "assistant" && m.Usage != nil {
			found = true
			total.Input += m.Usage.Input
			total.Output += m.Usage.Output
			total.CacheRead += m.Usage.CacheRead
			total.CacheWrite += m.Usage.CacheWrite
			total.TotalTokens += m.Usage.TotalTokens
		}
	}
	if !found {
		return nil
	}
	return &total
}

// childCost computes the USD cost of a child's usage using the CHILD's model
// pricing (which may differ from the parent's).
func childCost(model core.Model, msgs []core.AgentMessage) float64 {
	if model.Pricing == nil {
		return 0
	}
	var cost float64
	for _, m := range msgs {
		if m.Role == "assistant" && m.Usage != nil {
			cost += model.Pricing.Cost(*m.Usage)
		}
	}
	return cost
}

// resolveChildGuardrails applies defaults for the child's own turn/duration
// limits, independent of the parent's guardrails and with no budget ($)
// limit (children never get a MaxBudget). A positive perCallDuration overrides
// the configured/default wall-clock budget (from the tool's max_duration arg).
func resolveChildGuardrails(cfg Config, perCallDuration time.Duration) (maxTurns int, maxRunDuration time.Duration) {
	maxTurns = cfg.ChildMaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultChildMaxTurns
	}
	switch {
	case perCallDuration > 0:
		maxRunDuration = perCallDuration
	case cfg.ChildMaxRunDuration > 0:
		maxRunDuration = cfg.ChildMaxRunDuration
	default:
		maxRunDuration = defaultChildMaxRunDuration
	}
	return maxTurns, maxRunDuration
}

func newChildAgent(cfg Config, provider core.Provider, model core.Model, thinkingLevel string, maxRunDuration time.Duration, systemPrompt string, childReg *core.Registry) (*agent.Agent, error) {
	maxTurns, runDuration := resolveChildGuardrails(cfg, maxRunDuration)
	return agent.New(agent.AgentConfig{
		Provider:       provider,
		Model:          model,
		SystemPrompt:   systemPrompt,
		ThinkingLevel:  thinkingLevel,
		Tools:          childReg,
		MaxTurns:       maxTurns,
		MaxRunDuration: runDuration,
		// No MaxBudget: children have no $ guardrail of their own (they run under
		// the parent's own budget, if any).
		PermissionCheck: func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			if fn := currentPermissionCheck(cfg); fn != nil {
				return fn(ctx, name, args)
			}
			return nil
		},
		// Compaction was off when the child's turn budget was low (<=30):
		// a short, focused child was unlikely to blow its context before
		// exhausting turns. Now that defaultChildMaxTurns is 100, a child on
		// a long task could realistically hit the context window first, so
		// compaction is enabled with the same defaults as the main session.
		Compaction: &core.CompactionSettings{
			Enabled:       true,
			ReserveTokens: core.DefaultCompactionSettings.ReserveTokens,
			KeepRecent:    core.DefaultCompactionSettings.KeepRecent,
		},
	})
}

func buildChildRegistry(parent *core.Registry, params map[string]any) (*core.Registry, *core.Result) {
	if parent == nil {
		res := core.ErrorResult("subagent parent tools are not configured")
		return nil, &res
	}

	allowed := make(map[string]core.Tool)
	for _, t := range parent.All() {
		if excludedTools[t.Name] {
			continue
		}
		allowed[t.Name] = t
	}

	reg := core.NewRegistry()
	selected, ok := params["tools"]
	if !ok {
		for _, t := range allowed {
			core.RegisterOrLog(reg, t)
		}
		return reg, nil
	}

	arr, ok := selected.([]any)
	if !ok {
		res := core.ErrorResult("tools must be an array of strings")
		return nil, &res
	}
	if len(arr) == 0 {
		res := core.ErrorResult("tools array cannot be empty")
		return nil, &res
	}

	seen := make(map[string]bool)
	for _, item := range arr {
		name, ok := item.(string)
		if !ok {
			res := core.ErrorResult("tools must be an array of strings")
			return nil, &res
		}
		// Normalize: the model may use Claude Code casing ("Read", "Bash")
		// but the registry uses lowercase ("read", "bash").
		name = strings.ToLower(name)
		if seen[name] {
			continue
		}
		t, ok := allowed[name]
		if !ok {
			// Silently skip excluded tools (subagent, subagent_status, etc.)
			// — the model naturally includes them since it sees them in its
			// own toolset, but children can't use them.
			if excludedTools[name] {
				continue
			}
			res := core.ErrorResult("unknown tool: " + name)
			return nil, &res
		}
		seen[name] = true
		core.RegisterOrLog(reg, t)
	}
	return reg, nil
}

func resolveModel(defaultModel core.Model, params map[string]any) (core.Model, *core.Result) {
	modelSpec, _ := params["model"].(string)
	if strings.TrimSpace(modelSpec) == "" {
		return defaultModel, nil
	}
	model, ok := core.ResolveModel(modelSpec)
	if ok {
		return model, nil
	}
	if model.Provider == "" {
		res := core.ErrorResult("unknown model: " + modelSpec)
		return core.Model{}, &res
	}
	return model, nil
}

func resolveThinking(defaultThinking string, params map[string]any) (string, *core.Result) {
	thinking, _ := params["thinking"].(string)
	if strings.TrimSpace(thinking) == "" {
		return defaultThinking, nil
	}
	if core.IsValidThinkingLevel(thinking) {
		return thinking, nil
	}
	res := core.ErrorResult("invalid thinking level: " + thinking)
	return "", &res
}

// resolveMaxDuration parses the optional "max_duration" arg (a Go duration
// string). Returns 0 when absent (meaning "use the configured/default budget").
func resolveMaxDuration(params map[string]any) (time.Duration, *core.Result) {
	spec, _ := params["max_duration"].(string)
	if strings.TrimSpace(spec) == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(spec))
	if err != nil {
		res := core.ErrorResult("invalid max_duration (use a Go duration like \"20m\" or \"1h\"): " + err.Error())
		return 0, &res
	}
	if d <= 0 {
		res := core.ErrorResult("max_duration must be positive")
		return 0, &res
	}
	return d, nil
}

// resolveResume loads a prior subagent's transcript when the "resume" arg is
// set, returning the messages to seed the child with. Returns nil (no seed)
// when resume is absent. Errors are surfaced as tool results so the model gets
// a clear message (unknown job ID, resume unsupported, etc.).
func resolveResume(cfg Config, params map[string]any) ([]core.AgentMessage, *core.Result) {
	jobID, _ := params["resume"].(string)
	if strings.TrimSpace(jobID) == "" {
		return nil, nil
	}
	if cfg.TranscriptLoader == nil {
		res := core.ErrorResult("resume is not supported in this environment")
		return nil, &res
	}
	msgs, err := cfg.TranscriptLoader(strings.TrimSpace(jobID))
	if err != nil {
		res := core.ErrorResult("cannot resume subagent " + jobID + ": " + err.Error())
		return nil, &res
	}
	if len(msgs) == 0 {
		res := core.ErrorResult("cannot resume subagent " + jobID + ": transcript is empty")
		return nil, &res
	}
	clean := sanitizeResumeTranscript(msgs)
	if len(clean) == 0 {
		res := core.ErrorResult("cannot resume subagent " + jobID + ": transcript has no replayable messages")
		return nil, &res
	}
	return clean, nil
}

// sanitizeResumeTranscript makes a persisted transcript safe to replay before a
// new user message is appended (LoadMessages + Send). It:
//   - strips thinking blocks (their signatures are model-specific; replaying
//     them, possibly under a different model, causes provider errors);
//   - drops assistant messages left empty after stripping;
//   - trims a trailing assistant turn whose tool_call(s) never received their
//     tool_result — a transcript cut off mid-turn (the exact failure mode of a
//     timed-out subagent) would otherwise send an orphaned tool_call followed by
//     a user message, which providers reject.
//
// Returns a new outer slice; content slices are rebuilt only for the messages
// it modifies, and no shared slice is mutated in place, so the input transcript
// is left untouched.
func sanitizeResumeTranscript(msgs []core.AgentMessage) []core.AgentMessage {
	out := make([]core.AgentMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != "assistant" {
			out = append(out, m)
			continue
		}
		hasThinking := false
		for _, c := range m.Content {
			if c.Type == "thinking" {
				hasThinking = true
				break
			}
		}
		if hasThinking {
			filtered := make([]core.Content, 0, len(m.Content))
			for _, c := range m.Content {
				if c.Type != "thinking" {
					filtered = append(filtered, c)
				}
			}
			m.Content = filtered
		}
		// Drop an assistant message that has no content left (e.g. it was
		// thinking-only) — an empty assistant turn is an invalid payload.
		if len(m.Content) == 0 {
			continue
		}
		out = append(out, m)
	}

	// Trim trailing turns until every tool_call has a matching tool_result. We
	// collect the result IDs first, then drop any trailing assistant message
	// that contains an unmatched tool_call (along with anything after it).
	resultIDs := make(map[string]bool)
	for _, m := range out {
		if m.Role == "tool_result" && m.ToolCallID != "" {
			resultIDs[m.ToolCallID] = true
		}
	}
	cut := len(out)
	for i := len(out) - 1; i >= 0; i-- {
		m := out[i]
		orphan := false
		if m.Role == "assistant" {
			for _, c := range m.Content {
				if c.Type == "tool_call" && !resultIDs[c.ToolCallID] {
					orphan = true
					break
				}
			}
		}
		if orphan {
			cut = i // drop this assistant turn and everything after it
			continue
		}
		// Stop at the first turn from the end that is fully satisfied.
		if m.Role == "assistant" {
			break
		}
	}
	out = out[:cut]

	// A replayable conversation must begin with a user message (a leading
	// tool_result or assistant is an invalid payload, and providers reject a
	// history that doesn't start with user). Drop any leading non-user messages
	// — e.g. a transcript that was itself persisted mid-conversation.
	start := 0
	for start < len(out) && out[start].Role != "user" {
		start++
	}
	return out[start:]
}

// runChild starts a child agent's loop, either fresh (Run) or continuing a
// resumed transcript (LoadMessages + Send). Resume replays the persisted
// history and sends task as the next user message.
func runChild(ctx context.Context, child *agent.Agent, task string, seedMsgs []core.AgentMessage) ([]core.AgentMessage, error) {
	if len(seedMsgs) == 0 {
		return child.Run(ctx, task)
	}
	if err := child.LoadMessages(seedMsgs); err != nil {
		return nil, err
	}
	return child.Send(ctx, task)
}

func buildSystemPrompt(promptBuilder func(agentcontext.SystemPromptOptions) string, agentsMD string, specs []core.ToolSpec, cwd, skillsIndex, memoryIndex string) string {
	const preamble = "You are a focused subagent. Complete the delegated task thoroughly and report your findings concisely. Do not ask clarifying questions — work with what you have.\n\n"
	return preamble + promptBuilder(agentcontext.SystemPromptOptions{
		AgentsMD:    agentsMD,
		Tools:       specs,
		CWD:         cwd,
		SkillsIndex: skillsIndex,
		MemoryIndex: memoryIndex,
	})
}

func forwardSyncEvent(e core.AgentEvent, onUpdate func(core.Result)) {
	if onUpdate == nil {
		return
	}
	switch e.Type {
	case core.AgentEventToolExecStart:
		onUpdate(core.TextResult("\n[subagent] Running " + e.ToolName + ": " + tool.SummarizeArgs(e.Args) + "\n"))
	case core.AgentEventToolExecEnd:
		prefix := "✓"
		if e.IsError {
			prefix = "✗"
		}
		onUpdate(core.TextResult("\n[subagent] " + prefix + " " + e.ToolName + "\n"))
	case core.AgentEventMessageUpdate:
		if e.AssistantEvent == nil {
			return
		}
		if e.AssistantEvent.Type == core.ProviderEventTextDelta && e.AssistantEvent.Delta != "" {
			onUpdate(core.TextResult(e.AssistantEvent.Delta))
		}
	}
}

func forwardAsyncEvent(jobs *jobStore, jobID string, e core.AgentEvent) {
	switch e.Type {
	case core.AgentEventToolExecStart:
		jobs.addProgress(jobID, e.ToolName+": "+tool.SummarizeArgs(e.Args))
	case core.AgentEventToolExecEnd:
		status := "✓"
		if e.IsError {
			status = "✗"
		}
		jobs.addProgress(jobID, status+" "+e.ToolName)
	}
}

func formatStatus(snap jobSnapshot) string {
	var sb strings.Builder
	sb.WriteString("Status: ")
	sb.WriteString(snap.Status)

	switch snap.Status {
	case statusRunning, statusCancelling:
		sb.WriteString("\nTask: ")
		sb.WriteString(snap.Task)
		sb.WriteString("\nModel: ")
		sb.WriteString(snap.Model)
		if line, ok := formatUsageLine(snap); ok {
			sb.WriteString("\n")
			sb.WriteString(line)
		}
		if len(snap.Progress) > 0 {
			sb.WriteString("\nRecent activity:")
			for _, line := range snap.Progress {
				sb.WriteString("\n- ")
				sb.WriteString(line)
			}
		}
	case statusCompleted:
		if line, ok := formatUsageLine(snap); ok {
			sb.WriteString("\n")
			sb.WriteString(line)
		}
		sb.WriteString("\n\nResult:\n")
		sb.WriteString(snap.Result)
	case statusFailed:
		sb.WriteString("\nError: ")
		sb.WriteString(snap.Error)
	}

	return sb.String()
}

// formatUsageLine renders "Tokens: <in>/<out>  Cost: $X.XXXX" for a job
// snapshot that has usage recorded. Returns ok=false when there is nothing
// to show (no usage captured yet).
func formatUsageLine(snap jobSnapshot) (string, bool) {
	if snap.Usage == nil {
		return "", false
	}
	return fmt.Sprintf("Tokens: %d/%d  Cost: $%.4f", snap.Usage.Input, snap.Usage.Output, snap.CostUSD), true
}

func currentModel(cfg Config) core.Model {
	if cfg.CurrentModel != nil {
		return cfg.CurrentModel()
	}
	return cfg.DefaultModel
}

func currentThinkingLevel(cfg Config) string {
	if cfg.CurrentThinkingLevel != nil {
		return cfg.CurrentThinkingLevel()
	}
	return "medium"
}

func currentPermissionCheck(cfg Config) func(context.Context, string, map[string]any) *core.ToolCallDecision {
	if cfg.CurrentPermissionCheck != nil {
		return cfg.CurrentPermissionCheck()
	}
	return nil
}

// tailLinesWithFlag returns the last n lines and whether truncation occurred.
func tailLinesWithFlag(s string, n int) (string, bool) {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s, false
	}
	return strings.Join(lines[len(lines)-n:], "\n"), true
}

func getBool(params map[string]any, key string) bool {
	v, ok := params[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}
