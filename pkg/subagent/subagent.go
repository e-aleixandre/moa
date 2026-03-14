package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ealeixandre/moa/pkg/agent"
	agentcontext "github.com/ealeixandre/moa/pkg/context"
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tool"
)

const (
	jobTTL = 30 * time.Minute

	defaultMaxTurns            = 50
	defaultMaxToolCallsPerTurn = 20
	defaultMaxRunDuration      = 10 * time.Minute

	// asyncResultTailLines is how many trailing lines of a completed async
	// subagent result are included in the notification to the parent.
	asyncResultTailLines = 50
)

// excludedTools prevents recursive subagent spawning.
// Subagents are leaf workers, not orchestrators.
var excludedTools = map[string]bool{
	"subagent":        true,
	"subagent_status": true,
	"subagent_cancel": true,
}

type Config struct {
	DefaultModel           core.Model
	CurrentModel           func() core.Model
	CurrentThinkingLevel   func() string
	CurrentPermissionCheck func() func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision
	ProviderFactory        func(core.Model) (core.Provider, error)
	AgentsMD               string
	PromptBuilder          func(agentsMD string, toolSpecs []core.ToolSpec, cwd string, hasVerify bool, skillsIndex ...string) string
	ParentTools            *core.Registry
	AppCtx                 context.Context
	WorkspaceRoot          string // CWD passed to system prompt builder
	SkillsIndex            string // pre-formatted skills index for system prompt

	// OnAsyncComplete is called when an async subagent finishes (completed, failed, or cancelled).
	OnAsyncComplete func(jobID, task, status, resultTail string)

	// OnAsyncJobChange is called when an async job starts or finishes.
	// count is the current number of running jobs.
	OnAsyncJobChange func(count int)
}

func RegisterAll(reg *core.Registry, cfg Config) error {
	if cfg.AppCtx == nil {
		return errors.New("subagent: AppCtx is required")
	}
	jobs := newJobStore()
	for _, t := range []core.Tool{
		newSubagent(cfg, jobs),
		newSubagentStatus(jobs),
		newSubagentCancel(jobs),
	} {
		if err := reg.Register(t); err != nil {
			return fmt.Errorf("subagent: %w", err)
		}
	}
	return nil
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
					"description": "Thinking level for the subagent: off, minimal, low, medium, high. Defaults to the current parent thinking level."
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
			systemPrompt := buildSystemPrompt(promptBuilder, cfg.AgentsMD, childReg.Specs(), cfg.WorkspaceRoot, cfg.SkillsIndex)

			if getBool(params, "async") {
				if err := ctx.Err(); err != nil {
					return core.ErrorResult(err.Error()), nil
				}
				jobCtx, jobCancel := context.WithCancel(cfg.AppCtx)
				job := jobs.create(task, model.ID, jobCancel)
				if cfg.OnAsyncJobChange != nil {
					cfg.OnAsyncJobChange(jobs.runningCount())
				}
				go runAsyncJob(jobCtx, cfg, jobs, job, provider, model, thinkingLevel, systemPrompt, childReg, task)
				return core.TextResult("Subagent started in background.\nJob ID: " + job.id + "\nUse subagent_status to check progress, subagent_cancel to stop."), nil
			}

			return runSync(ctx, cfg, provider, model, thinkingLevel, systemPrompt, childReg, task, onUpdate)
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

func runSync(ctx context.Context, cfg Config, provider core.Provider, model core.Model, thinkingLevel string, systemPrompt string, childReg *core.Registry, task string, onUpdate func(core.Result)) (core.Result, error) {
	child, err := newChildAgent(cfg, provider, model, thinkingLevel, systemPrompt, childReg)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	unsub := child.Subscribe(func(e core.AgentEvent) {
		forwardSyncEvent(e, onUpdate)
	})
	defer unsub()

	msgs, err := child.Run(ctx, task)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	return core.TextResult(core.ExtractFinalAssistantText(msgs)), nil
}

func runAsyncJob(jobCtx context.Context, cfg Config, jobs *jobStore, j *job, provider core.Provider, model core.Model, thinkingLevel string, systemPrompt string, childReg *core.Registry, task string) {
	defer j.cancel()
	defer close(j.done)
	defer func() {
		if cfg.OnAsyncComplete != nil {
			snap, ok := jobs.snapshot(j.id)
			if !ok {
				return
			}
			cfg.OnAsyncComplete(snap.ID, snap.Task, snap.Status, tailLines(snap.Result, asyncResultTailLines))
		}
		if cfg.OnAsyncJobChange != nil {
			cfg.OnAsyncJobChange(jobs.runningCount())
		}
	}()

	child, err := newChildAgent(cfg, provider, model, thinkingLevel, systemPrompt, childReg)
	if err != nil {
		jobs.setFailed(j.id, err.Error())
		return
	}
	unsub := child.Subscribe(func(e core.AgentEvent) {
		forwardAsyncEvent(jobs, j.id, e)
	})
	defer unsub()

	msgs, err := child.Run(jobCtx, task)
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

func newChildAgent(cfg Config, provider core.Provider, model core.Model, thinkingLevel string, systemPrompt string, childReg *core.Registry) (*agent.Agent, error) {
	return agent.New(agent.AgentConfig{
		Provider:            provider,
		Model:               model,
		SystemPrompt:        systemPrompt,
		ThinkingLevel:       thinkingLevel,
		Tools:               childReg,
		MaxTurns:            defaultMaxTurns,
		MaxToolCallsPerTurn: defaultMaxToolCallsPerTurn,
		MaxRunDuration:      defaultMaxRunDuration,
		PermissionCheck: func(ctx context.Context, name string, args map[string]any) *core.ToolCallDecision {
			if fn := currentPermissionCheck(cfg); fn != nil {
				return fn(ctx, name, args)
			}
			return nil
		},
		// Subagents run short, focused tasks — compaction adds complexity
		// without benefit. If maxTurns increases significantly, revisit.
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
	switch thinking {
	case "off", "minimal", "low", "medium", "high":
		return thinking, nil
	default:
		res := core.ErrorResult("invalid thinking level: " + thinking)
		return "", &res
	}
}

func buildSystemPrompt(promptBuilder func(string, []core.ToolSpec, string, bool, ...string) string, agentsMD string, specs []core.ToolSpec, cwd, skillsIndex string) string {
	const preamble = "You are a focused subagent. Complete the delegated task thoroughly and report your findings concisely. Do not ask clarifying questions — work with what you have.\n\n"
	return preamble + promptBuilder(agentsMD, specs, cwd, false, skillsIndex)
}

func forwardSyncEvent(e core.AgentEvent, onUpdate func(core.Result)) {
	if onUpdate == nil {
		return
	}
	switch e.Type {
	case core.AgentEventToolExecStart:
		onUpdate(core.TextResult("[subagent] Running " + e.ToolName + ": " + tool.SummarizeArgs(e.Args)))
	case core.AgentEventToolExecEnd:
		prefix := "✓"
		if e.IsError {
			prefix = "✗"
		}
		onUpdate(core.TextResult("[subagent] " + prefix + " " + e.ToolName))
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
		if len(snap.Progress) > 0 {
			sb.WriteString("\nRecent activity:")
			for _, line := range snap.Progress {
				sb.WriteString("\n- ")
				sb.WriteString(line)
			}
		}
	case statusCompleted:
		sb.WriteString("\n\nResult:\n")
		sb.WriteString(snap.Result)
	case statusFailed:
		sb.WriteString("\nError: ")
		sb.WriteString(snap.Error)
	}

	return sb.String()
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

// tailLines returns the last n lines of s.
func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func getBool(params map[string]any, key string) bool {
	v, ok := params[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}
