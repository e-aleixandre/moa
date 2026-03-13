package serve

import "github.com/ealeixandre/moa/pkg/core"

// Event is a JSON-serializable event sent to WebSocket clients.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
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
	PendingPermission *PermissionData     `json:"pending_permission,omitempty"`
	PendingAsk        *AskData            `json:"pending_ask,omitempty"`
	Tasks             any                 `json:"tasks,omitempty"`
	PlanMode          string              `json:"plan_mode,omitempty"`
	PlanFile          string              `json:"plan_file,omitempty"`
}

// PermissionData is a pending permission request.
type PermissionData struct {
	ID       string         `json:"id"`
	ToolName string         `json:"tool_name"`
	Args     map[string]any `json:"args"`
}

// AskData is a pending ask_user request.
type AskData struct {
	ID        string `json:"id"`
	Questions []askQ `json:"questions"`
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
	Text string `json:"text"`
}

// ToolStartData is sent when a tool execution begins.
type ToolStartData struct {
	ToolCallID string         `json:"tool_call_id"`
	ToolName   string         `json:"tool_name"`
	Args       map[string]any `json:"args"`
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

// SteerData is sent when the user steers a running agent.
type SteerData struct {
	Text string `json:"text"`
}

// PlanModeData is sent on plan mode state changes.
type PlanModeData struct {
	Mode     string `json:"mode"`
	PlanFile string `json:"plan_file,omitempty"`
}

// CommandData is sent when a slash command is executed.
type CommandData struct {
	Command  string              `json:"command"`
	Messages []core.AgentMessage `json:"messages,omitempty"` // compact sends updated messages
}

// ConfigChangeData is sent when model/thinking/permissions change.
type ConfigChangeData struct {
	Model          string `json:"model,omitempty"`
	Thinking       string `json:"thinking,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
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
