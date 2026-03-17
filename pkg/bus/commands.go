package bus

import "github.com/ealeixandre/moa/pkg/core"

// ---------------------------------------------------------------------------
// Agent interaction
// ---------------------------------------------------------------------------

// SendPrompt starts an agent run with a text prompt.
// If Custom is non-nil, SendWithCustom is used instead of Send.
type SendPrompt struct {
	SessionID string
	Text      string
	Custom    map[string]any
}

// SendPromptWithContent starts an agent run with structured content (e.g. images).
type SendPromptWithContent struct {
	SessionID string
	Content   []core.Content
}

// SteerAgent injects a steering message into a running agent.
type SteerAgent struct {
	SessionID string
	Text      string
}

// AppendToConversation adds a message to the conversation without running the agent.
type AppendToConversation struct {
	SessionID string
	Message   core.AgentMessage
}

// AbortRun cancels a running agent.
type AbortRun struct{ SessionID string }

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// SwitchModel changes the active model.
type SwitchModel struct {
	SessionID string
	ModelSpec string
}

// SetThinking changes the thinking level.
type SetThinking struct {
	SessionID string
	Level     string
}

// SetPermissionMode changes the permission mode (yolo/ask/auto).
type SetPermissionMode struct {
	SessionID string
	Mode      string
}

// ---------------------------------------------------------------------------
// Path policy
// ---------------------------------------------------------------------------

// SetPathScope changes workspace/unrestricted scope.
type SetPathScope struct {
	SessionID string
	Scope     string
}

// AddAllowedPath adds a directory to allowed paths.
type AddAllowedPath struct {
	SessionID string
	Path      string
}

// RemoveAllowedPath removes a directory from allowed paths.
type RemoveAllowedPath struct {
	SessionID string
	Path      string
}

// ---------------------------------------------------------------------------
// Session lifecycle
// ---------------------------------------------------------------------------

// ClearSession resets the conversation (agent.Reset).
type ClearSession struct{ SessionID string }

// CompactSession triggers manual compaction.
type CompactSession struct{ SessionID string }

// UndoLastChange pops the last checkpoint and restores files.
type UndoLastChange struct{ SessionID string }

// ---------------------------------------------------------------------------
// Plan mode
// ---------------------------------------------------------------------------

// EnterPlanMode enters planning mode (creates plan file).
type EnterPlanMode struct{ SessionID string }

// ExitPlanMode exits planning mode.
type ExitPlanMode struct{ SessionID string }

// StartPlanExecution transitions from ready → executing.
// CleanContext controls whether the conversation is reset before execution.
type StartPlanExecution struct {
	SessionID    string
	CleanContext bool
}

// StartPlanReview transitions from ready → reviewing.
// Review configuration (model, thinking) is handled by the TUI locally,
// not by the bus — the plan mode state machine only tracks the mode transition.
type StartPlanReview struct {
	SessionID string
}

// ContinueRefining transitions from reviewing → planning (continue refining).
type ContinueRefining struct{ SessionID string }

// FinishPlanReview completes the review phase and transitions to ready.
type FinishPlanReview struct{ SessionID string }

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

// MarkTaskDone marks a task as done.
type MarkTaskDone struct {
	SessionID string
	TaskID    int
}

// ResetTasks clears all tasks.
type ResetTasks struct{ SessionID string }

// ---------------------------------------------------------------------------
// Approvals
// ---------------------------------------------------------------------------

// ResolvePermission resolves a pending tool permission request.
type ResolvePermission struct {
	SessionID    string
	PermissionID string
	Approved     bool
	Feedback     string
	AllowPattern string
}

// AddPermissionRule adds a natural-language rule to auto-mode while a
// permission request is pending. The request stays open — the user can
// still approve/deny. This is NOT "always allow this request".
type AddPermissionRule struct {
	SessionID    string
	PermissionID string
	Rule         string
}

// ResolveAskUser resolves a pending ask_user prompt.
type ResolveAskUser struct {
	SessionID string
	AskID     string
	Answers   []string
}
