package serve

import (
	"github.com/ealeixandre/moa/pkg/bus"
	"github.com/ealeixandre/moa/pkg/core"
)

// Event is a JSON-serializable event sent to WebSocket clients.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
	Seq  uint64 `json:"seq,omitempty"`
}

// --- Typed event data structs ---
// Each struct corresponds to an event type's Data payload. Using typed structs
// instead of map[string]any catches key typos and type mismatches at compile time.

// InitData is sent on WebSocket connect with the full session state.
type InitData struct {
	Messages          []core.AgentMessage `json:"messages"`
	State             string              `json:"state"`
	ContextPercent    int                 `json:"context_percent"`
	PermissionMode    string              `json:"permission_mode"`
	PathScope         string              `json:"path_scope,omitempty"`
	PendingPermission *PermissionData     `json:"pending_permission,omitempty"`
	PendingAsk        *AskData            `json:"pending_ask,omitempty"`
	Tasks             any                 `json:"tasks,omitempty"`
	PlanMode          string              `json:"plan_mode,omitempty"`
	PlanFile          string              `json:"plan_file,omitempty"`
	GoalActive        bool                `json:"goal_active,omitempty"`
	GoalObjective     string              `json:"goal_objective,omitempty"`
	GoalWorkDir       string              `json:"goal_work_dir,omitempty"`
	GoalIteration     int                 `json:"goal_iteration,omitempty"`
	GoalStalled       int                 `json:"goal_stalled,omitempty"`
	GoalVerifying     bool                `json:"goal_verifying,omitempty"`
	Compacting        bool                `json:"compacting,omitempty"`
	StreamingText     string              `json:"streaming_text,omitempty"`
	StreamingThinking string              `json:"streaming_thinking,omitempty"`
	PendingSteers     []PendingSteerData  `json:"pending_steers,omitempty"`
	CostUSD           float64             `json:"cost_usd,omitempty"`
	Subagents         []SubagentInitData  `json:"subagents,omitempty"`
	BashJobs          []BashJobInitData   `json:"bash_jobs,omitempty"`
	LastSeq           uint64              `json:"last_seq,omitempty"`
	HistoryTruncated  bool                `json:"history_truncated,omitempty"`
}

// PendingSteerData is one queued (not yet delivered) steer message, with its
// authoritative ID so a reconnecting client reconciles its optimistic chip by
// ID instead of by text.
type PendingSteerData struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// SubagentInitData describes one live subagent job for reconnecting clients
// (WS init snapshot), so a client that connects mid-run sees the agent tray
// and its accumulated transcript instead of starting empty.
type SubagentInitData struct {
	JobID    string              `json:"job_id"`
	Task     string              `json:"task"`
	Model    string              `json:"model"`
	Status   string              `json:"status"`
	Async    bool                `json:"async"`
	Messages []core.AgentMessage `json:"messages"`
}

// BashJobInitData restores a live/recent background command after reconnect.
type BashJobInitData struct {
	JobID   string `json:"job_id"`
	Command string `json:"command"`
	CWD     string `json:"cwd"`
	Status  string `json:"status"`
	Output  string `json:"output"`
}

// PermissionData is a pending permission request.
type PermissionData struct {
	ID           string         `json:"id"`
	ToolName     string         `json:"tool_name"`
	Args         map[string]any `json:"args"`
	AllowPattern string         `json:"allow_pattern,omitempty"`
}

// AskData is a pending ask_user request.
type AskData struct {
	ID        string            `json:"id"`
	Questions []bus.AskQuestion `json:"questions"`
}

// StateChangeData is sent when the session state changes.
type StateChangeData struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// DeltaData carries a streaming text delta.
type DeltaData struct {
	Delta string `json:"delta"`
}

// MessageEndData carries the full assistant text on message completion.
type MessageEndData struct {
	Text  string `json:"text"`
	MsgID string `json:"msg_id,omitempty"`
}

// ToolStartData is sent when a tool execution begins.
// ToolCallStreamingData is sent when the LLM starts generating a tool call.
type ToolCallStreamingData struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
}

// ToolCallDeltaData carries incrementally-parsed tool call arguments.
type ToolCallDeltaData struct {
	ToolCallID string         `json:"tool_call_id"`
	Args       map[string]any `json:"args"`
}

type ToolStartData struct {
	ToolCallID string         `json:"tool_call_id"`
	ToolName   string         `json:"tool_name"`
	Args       map[string]any `json:"args"`
	// StartLine is the real 1-based file line where an edit's oldText starts,
	// so the frontend diff preview shows real line numbers before the tool
	// result arrives. 0 when unknown (frontend numbers from 1). Edit tool only.
	StartLine int `json:"start_line,omitempty"`
}

// ToolUpdateData carries streaming tool output.
type ToolUpdateData struct {
	ToolCallID string `json:"tool_call_id"`
	Delta      string `json:"delta"`
}

// ToolEndData is sent when a tool execution completes.
type ToolEndData struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	IsError    bool   `json:"is_error"`
	Rejected   bool   `json:"rejected"`
	Result     string `json:"result"`
}

// TasksUpdateData carries the full task list after a change.
type TasksUpdateData struct {
	Tasks any `json:"tasks"`
}

// RunEndData carries the final assistant text when a run completes.
type RunEndData struct {
	Text string `json:"text"`
}

// ContextUpdateData carries the current context usage percentage.
type ContextUpdateData struct {
	ContextPercent int `json:"context_percent"`
}

// SessionCostData carries the accumulated session spend (main run + subagents).
type SessionCostData struct {
	CostUSD float64 `json:"cost_usd"`
}

// RateLimitData carries the provider's per-request rate-limit state: plan-window
// utilization (as percentages, to match /api/usage) and whether this request
// was served from extra usage.
type RateLimitData struct {
	Status              string `json:"status,omitempty"`
	RepresentativeClaim string `json:"representative_claim,omitempty"`
	OnOverage           bool   `json:"on_overage"`
	FiveHourPct         int    `json:"five_hour_pct"`
	SevenDayPct         int    `json:"seven_day_pct"`
	OveragePct          int    `json:"overage_pct"`
}

// pctOf converts a [0,1] utilization fraction to a rounded percentage, or -1
// when the fraction is unknown (negative sentinel from the parser).
func pctOf(frac float64) int {
	if frac < 0 {
		return -1
	}
	return int(frac*100 + 0.5)
}

// SteerData is sent when the user steers a running agent.
type SteerData struct {
	ID    string   `json:"id,omitempty"`
	IDs   []string `json:"ids,omitempty"`
	MsgID string   `json:"msg_id,omitempty"`
	Text  string   `json:"text"`
}

// PlanModeData is sent on plan mode state changes.
type PlanModeData struct {
	Mode     string `json:"mode"`
	PlanFile string `json:"plan_file,omitempty"`
}

// GoalChangeData is sent when goal mode activates or deactivates.
type GoalChangeData struct {
	Active    bool   `json:"active"`
	Objective string `json:"objective,omitempty"`
	WorkDir   string `json:"work_dir,omitempty"`
	Iteration int    `json:"iteration"`
	Stalled   int    `json:"stalled"`
}

// GoalIterationData is sent after the verifier judges a goal iteration.
type GoalIterationData struct {
	Iteration int    `json:"iteration"`
	Satisfied bool   `json:"satisfied"`
	Feedback  string `json:"feedback,omitempty"`
}

// GoalEndData is sent when a goal loop ends.
type GoalEndData struct {
	Reason string `json:"reason"`
}

// CommandData is sent when a slash command is executed.
type CommandData struct {
	Command          string              `json:"command"`
	Messages         []core.AgentMessage `json:"messages,omitempty"` // compact sends updated messages
	HistoryTruncated bool                `json:"history_truncated,omitempty"`
}

// ConfigChangeData is sent when model/thinking/permissions/path scope change.
type ConfigChangeData struct {
	Model          string `json:"model,omitempty"`
	Provider       string `json:"provider,omitempty"`
	Thinking       string `json:"thinking,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	PathScope      string `json:"path_scope,omitempty"`
}

// SubagentCountData is sent when async subagent jobs start/finish.
type SubagentCountData struct {
	Count int `json:"count"`
}

// SubagentCompleteData is sent when an async subagent finishes.
type SubagentCompleteData struct {
	JobID  string `json:"job_id"`
	Task   string `json:"task"`
	Status string `json:"status"`
	Text   string `json:"text"`
}

// SubagentStartData is sent when a subagent (sync or async) begins.
type SubagentStartData struct {
	JobID string `json:"job_id"`
	Task  string `json:"task"`
	Model string `json:"model"`
	Async bool   `json:"async"`
}

// SubagentEndData is sent when a subagent finishes, carrying its usage/cost.
type SubagentEndData struct {
	JobID        string  `json:"job_id"`
	Status       string  `json:"status"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// SubagentEventData wraps a single translated bus event from a subagent
// child, namespaced by JobID. Event is produced by re-applying
// wsEventFromBus to the inner (already-typed) bus event — same shape as a
// top-level WS event.
type SubagentEventData struct {
	JobID string `json:"job_id"`
	Event *Event `json:"event"`
}

type BashJobStartData struct {
	JobID   string `json:"job_id"`
	Command string `json:"command"`
	CWD     string `json:"cwd"`
}

type BashJobOutputData struct {
	JobID string `json:"job_id"`
	Delta string `json:"delta"`
}

type BashJobEndData struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
	Output string `json:"output"`
}

// BashCompleteData is sent when an async background bash job finishes and its
// formatted result is reinjected into the conversation.
type BashCompleteData struct {
	JobID   string `json:"job_id"`
	Command string `json:"command"`
	Status  string `json:"status"`
	Text    string `json:"text"`
}
