// Package bus provides a typed event bus for decoupling agent, TUI, and serve layers.
//
// Events are fire-and-forget (async fan-out to subscribers).
// Commands are synchronous request→response (one handler per type).
// Queries are synchronous read-only requests (one handler per type).
//
// Top-level event/command/query payloads must be non-nil value structs.
// Nested fields may contain pointers, slices, and maps — subscribers must
// treat all payloads as read-only (no mutation after publish).
package bus

import (
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/tasks"
)

// ---------------------------------------------------------------------------
// Agent lifecycle
// ---------------------------------------------------------------------------

// AgentStarted is published when the agent loop begins.
type AgentStarted struct {
	SessionID string
	RunGen    uint64
}

// AgentEnded is published when the agent loop finishes normally.
type AgentEnded struct {
	SessionID string
	RunGen    uint64
	Messages  []core.AgentMessage
}

// AgentError is published when the agent loop exits with an error.
type AgentError struct {
	SessionID string
	RunGen    uint64
	Err       error
}

// ---------------------------------------------------------------------------
// Turn lifecycle
// ---------------------------------------------------------------------------

// TurnStarted is published at the start of each agent turn (LLM call).
type TurnStarted struct {
	SessionID string
	RunGen    uint64
}

// TurnEnded is published at the end of each agent turn.
type TurnEnded struct {
	SessionID string
	RunGen    uint64
}

// ---------------------------------------------------------------------------
// Message streaming
// ---------------------------------------------------------------------------

// MessageStarted is published when a new assistant message begins streaming.
type MessageStarted struct {
	SessionID string
	RunGen    uint64
	Message   core.AgentMessage
}

// TextDelta is published for each text chunk streamed from the model.
type TextDelta struct {
	SessionID string
	RunGen    uint64
	Delta     string
}

// ThinkingDelta is published for each thinking/reasoning chunk from the model.
type ThinkingDelta struct {
	SessionID string
	RunGen    uint64
	Delta     string
}

// MessageEnded is published when an assistant message finishes streaming.
type MessageEnded struct {
	SessionID string
	RunGen    uint64
	Message   core.AgentMessage
	FullText  string
}

// ---------------------------------------------------------------------------
// Tool execution
// ---------------------------------------------------------------------------

// ToolExecStarted is published when a tool call begins execution.
type ToolExecStarted struct {
	SessionID  string
	RunGen     uint64
	ToolCallID string
	ToolName   string
	Args       map[string]any
}

// ToolExecUpdate is published for streaming tool output.
type ToolExecUpdate struct {
	SessionID  string
	RunGen     uint64
	ToolCallID string
	Delta      string
}

// ToolExecEnded is published when a tool call finishes.
type ToolExecEnded struct {
	SessionID  string
	RunGen     uint64
	ToolCallID string
	ToolName   string
	Result     string
	IsError    bool
	Rejected   bool
}

// ---------------------------------------------------------------------------
// Compaction
// ---------------------------------------------------------------------------

// CompactionStarted is published when context compaction begins.
type CompactionStarted struct {
	SessionID string
	RunGen    uint64
}

// CompactionEnded is published when context compaction finishes.
type CompactionEnded struct {
	SessionID string
	RunGen    uint64
	Payload   *core.CompactionPayload
	Err       error
}

// ---------------------------------------------------------------------------
// Steering
// ---------------------------------------------------------------------------

// Steered is published when a steering message is injected into the agent.
type Steered struct {
	SessionID string
	RunGen    uint64
	Text      string
}

// ---------------------------------------------------------------------------
// Session state
// ---------------------------------------------------------------------------

// StateChanged is published when the session state transitions (idle/running/etc).
type StateChanged struct {
	SessionID string
	State     string
	Error     string
}

// RunEnded is published when a full agent run completes (may span multiple turns).
type RunEnded struct {
	SessionID string
	RunGen    uint64
	FinalText string
	Err       error // non-nil for real errors (not cancellation)
}

// ContextUpdated is published when the context window usage percentage changes.
type ContextUpdated struct {
	SessionID string
	Percent   int
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// ConfigChanged is published when session configuration changes (model, thinking, etc).
type ConfigChanged struct {
	SessionID      string
	Model          string
	Thinking       string
	PermissionMode string
	PathScope      string
}

// ---------------------------------------------------------------------------
// Plan mode
// ---------------------------------------------------------------------------

// PlanModeChanged is published when the plan mode state transitions.
type PlanModeChanged struct {
	SessionID string
	Mode      string // "off", "planning", "ready", "executing", "reviewing"
	PlanFile  string
}

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

// TasksUpdated is published when the task list changes.
type TasksUpdated struct {
	SessionID string
	Tasks     []tasks.Task
}

// ---------------------------------------------------------------------------
// Subagent
// ---------------------------------------------------------------------------

// SubagentCountChanged is published when the active subagent count changes.
type SubagentCountChanged struct {
	SessionID string
	Count     int
}

// SubagentCompleted is published when a subagent job finishes.
type SubagentCompleted struct {
	SessionID string
	JobID     string
	Task      string
	Status    string
	Text      string
}

// ---------------------------------------------------------------------------
// Permission
// ---------------------------------------------------------------------------

// PermissionRequested is published when a tool needs user approval.
type PermissionRequested struct {
	SessionID    string
	ID           string
	ToolName     string
	Args         map[string]any
	AllowPattern string // glob pattern for "always allow"
}

// PermissionResolved is published when a pending permission is resolved.
type PermissionResolved struct {
	SessionID string
	ID        string
}

// ---------------------------------------------------------------------------
// Ask user
// ---------------------------------------------------------------------------

// AskUserRequested is published when the agent needs user input.
type AskUserRequested struct {
	SessionID string
	ID        string
	Questions []AskQuestion
}

// AskUserResolved is published when a pending ask-user prompt is answered.
type AskUserResolved struct {
	SessionID string
	ID        string
}

// AskQuestion is a user-facing question with optional predefined answers.
type AskQuestion struct {
	Text    string   `json:"question"`
	Options []string `json:"options,omitempty"`
}

// ---------------------------------------------------------------------------
// Slash command
// ---------------------------------------------------------------------------

// CommandExecuted is published when a slash command is executed.
type CommandExecuted struct {
	SessionID string
	Command   string
	Messages  []core.AgentMessage // non-nil for /compact
}
