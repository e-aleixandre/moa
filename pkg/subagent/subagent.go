package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
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
	// Config.ChildMaxTurns is not set. Deliberately low and independent of
	// the parent's own MaxTurns: children are leaf workers with focused tasks.
	defaultChildMaxTurns = 30

	// defaultChildMaxRunDuration is the fallback per-child wall-clock budget
	// used when Config.ChildMaxRunDuration is not set.
	defaultChildMaxRunDuration = 10 * time.Minute

	// defaultMaxConcurrentAsync is the fallback cap on simultaneously running
	// async subagent jobs, used when Config.MaxConcurrentAsync is not set.
	defaultMaxConcurrentAsync = 5
)

// syncChildCounter yields unique agent IDs for synchronous subagents, which
// have no job ID. The "sync-" prefix keeps them distinct from async job IDs
// (random hex) and from the root agent ("").
var syncChildCounter atomic.Uint64

func nextSyncChildID() string {
	return fmt.Sprintf("sync-%d", syncChildCounter.Add(1))
}

// excludedTools prevents recursive subagent spawning.
// Subagents are leaf workers, not orchestrators.
var excludedTools = map[string]bool{
	"subagent":        true,
	"subagent_status": true,
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
	// running, with its jobID/task/model and whether it's async.
	OnChildStart func(jobID, task, model string, async bool)

	// OnChildEvent is called for each typed bus event produced by translating
	// the child's core.AgentEvent stream (via bus.TranslateAgentEvent). inner
	// is already a concrete bus.* type (e.g. bus.TextDelta), never a raw
	// core.AgentEvent — pkg/subagent imports pkg/bus directly (no import
	// cycle: pkg/bus does not import pkg/subagent), so translation happens
	// here rather than at the call site.
	OnChildEvent func(jobID string, inner any)

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
				"async": {
					"type": "boolean",
					"description": "Run in background and return a job ID. Use subagent_status to poll progress and subagent_cancel to stop."
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
				// job ID. runAsyncJob drops the snapshot when it finishes.
				if cfg.BashState != nil {
					cfg.BashState.Seed(job.id, core.AgentIDFromContext(ctx))
					jobCtx = core.WithAgentID(jobCtx, job.id)
				}
				if cfg.OnAsyncJobChange != nil {
					cfg.OnAsyncJobChange(jobs.runningCount())
				}
				go runAsyncJob(jobCtx, cfg, jobs, job, provider, model, thinkingLevel, systemPrompt, childReg, task)
				return core.TextResult("Subagent started in background.\nJob ID: " + job.id + "\nUse subagent_status to check progress, subagent_cancel to stop."), nil
			}

			// Isolate the child's shell state (subshell semantics): seed a copy
			// from the parent, tag the child ctx with an ephemeral ID, and drop
			// the snapshot when the sync run returns.
			childCtx := ctx
			if cfg.BashState != nil {
				childID := nextSyncChildID()
				cfg.BashState.Seed(childID, core.AgentIDFromContext(ctx))
				defer cfg.BashState.Drop(childID)
				childCtx = core.WithAgentID(ctx, childID)
			}
			jobCtx, jobCancel := context.WithCancel(childCtx)
			job := jobs.createSync(task, model.ID, jobCancel)
			defer jobCancel()
			return runSync(jobCtx, cfg, jobs, job, provider, model, thinkingLevel, systemPrompt, childReg, task, onUpdate)
		},
	}
}

func newSubagentStatus(jobs *jobStore) core.Tool {
	return core.Tool{
		Name:        "subagent_status",
		Label:       "Subagent Status",
		Description: "Check the status of an async subagent job.",
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

func runSync(ctx context.Context, cfg Config, jobs *jobStore, j *job, provider core.Provider, model core.Model, thinkingLevel string, systemPrompt string, childReg *core.Registry, task string, onUpdate func(core.Result)) (core.Result, error) {
	defer jobs.delete(j.id)

	child, err := newChildAgent(cfg, provider, model, thinkingLevel, systemPrompt, childReg)
	if err != nil {
		jobs.setFailed(j.id, err.Error())
		if cfg.OnChildEnd != nil {
			cfg.OnChildEnd(j.id, statusFailed, nil, 0)
		}
		return core.ErrorResult(err.Error()), nil
	}
	unsub := child.Subscribe(func(e core.AgentEvent) {
		forwardSyncEvent(e, onUpdate)
		forwardChildEvent(cfg, j.id, e)
		if e.Type == core.AgentEventMessageEnd {
			jobs.setMessages(j.id, child.Messages())
		}
	})
	defer unsub()

	if cfg.OnChildStart != nil {
		cfg.OnChildStart(j.id, task, model.ID, false)
	}

	msgs, err := child.Run(ctx, task)
	jobs.setMessages(j.id, msgs)
	usage, cost := childUsage(msgs), childCost(model, msgs)
	jobs.setUsage(j.id, usage, cost)
	if err != nil {
		status := statusFailed
		if errors.Is(err, context.Canceled) {
			status = statusCancelled
			jobs.setCancelled(j.id)
		} else {
			jobs.setFailed(j.id, err.Error())
		}
		if cfg.OnChildEnd != nil {
			cfg.OnChildEnd(j.id, status, usage, cost)
		}
		return core.ErrorResult(err.Error()), nil
	}
	jobs.setCompleted(j.id, core.ExtractFinalAssistantText(msgs))
	if cfg.OnChildEnd != nil {
		cfg.OnChildEnd(j.id, statusCompleted, usage, cost)
	}
	return core.TextResult(core.ExtractFinalAssistantText(msgs)), nil
}

func runAsyncJob(jobCtx context.Context, cfg Config, jobs *jobStore, j *job, provider core.Provider, model core.Model, thinkingLevel string, systemPrompt string, childReg *core.Registry, task string) {
	defer j.cancel()
	defer close(j.done)
	var finalMsgs []core.AgentMessage
	if cfg.BashState != nil {
		defer cfg.BashState.Drop(j.id)
	}
	defer func() {
		if cfg.OnAsyncComplete != nil {
			snap, ok := jobs.snapshot(j.id)
			if !ok {
				return
			}
			tail, wasTruncated := tailLinesWithFlag(snap.Result, asyncResultTailLines)
			cfg.OnAsyncComplete(snap.ID, snap.Task, snap.Status, tail, wasTruncated)
		}
		if cfg.OnAsyncJobChange != nil {
			cfg.OnAsyncJobChange(jobs.runningCount())
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

	child, err := newChildAgent(cfg, provider, model, thinkingLevel, systemPrompt, childReg)
	if err != nil {
		jobs.setFailed(j.id, err.Error())
		return
	}
	unsub := child.Subscribe(func(e core.AgentEvent) {
		forwardAsyncEvent(jobs, j.id, e)
		forwardChildEvent(cfg, j.id, e)
		if e.Type == core.AgentEventMessageEnd {
			jobs.setMessages(j.id, child.Messages())
		}
	})
	defer unsub()

	if cfg.OnChildStart != nil {
		cfg.OnChildStart(j.id, task, model.ID, true)
	}

	msgs, err := child.Run(jobCtx, task)
	finalMsgs = msgs
	jobs.setMessages(j.id, msgs)
	jobs.setUsage(j.id, childUsage(msgs), childCost(model, msgs))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			jobs.setCancelled(j.id)
			return
		}
		jobs.setFailed(j.id, err.Error())
		return
	}
	jobs.setCompleted(j.id, core.ExtractFinalAssistantText(msgs))
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
// limit (children never get a MaxBudget).
func resolveChildGuardrails(cfg Config) (maxTurns int, maxRunDuration time.Duration) {
	maxTurns = cfg.ChildMaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultChildMaxTurns
	}
	maxRunDuration = cfg.ChildMaxRunDuration
	if maxRunDuration <= 0 {
		maxRunDuration = defaultChildMaxRunDuration
	}
	return maxTurns, maxRunDuration
}

func newChildAgent(cfg Config, provider core.Provider, model core.Model, thinkingLevel string, systemPrompt string, childReg *core.Registry) (*agent.Agent, error) {
	maxTurns, maxRunDuration := resolveChildGuardrails(cfg)
	return agent.New(agent.AgentConfig{
		Provider:       provider,
		Model:          model,
		SystemPrompt:   systemPrompt,
		ThinkingLevel:  thinkingLevel,
		Tools:          childReg,
		MaxTurns:       maxTurns,
		MaxRunDuration: maxRunDuration,
		// No MaxBudget: children have no $ guardrail of their own (they run under
		// the parent's own budget, if any).
		PermissionCheck: func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			if fn := currentPermissionCheck(cfg); fn != nil {
				return fn(ctx, name, args)
			}
			return nil
		},
		// Compaction off is safe as long as MaxTurns stays low (<=30, the
		// default): a short, focused child is unlikely to blow its context
		// before exhausting turns, so compaction would only add complexity
		// without benefit. If ChildMaxTurns is raised significantly above the
		// default, revisit and enable compaction for the child too.
		Compaction: &core.CompactionSettings{Enabled: false},
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
