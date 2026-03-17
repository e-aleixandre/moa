package bus

// ---------------------------------------------------------------------------
// Query types
//
// Each query type documents its expected return type in a comment.
// Use QueryTyped[Q, R](bus, query) for type-safe results at call sites.
// ---------------------------------------------------------------------------

// GetMessages returns the current conversation messages.
// Handler returns: []core.AgentMessage
type GetMessages struct{ SessionID string }

// GetModel returns the current model configuration.
// Handler returns: core.Model
type GetModel struct{ SessionID string }

// GetThinkingLevel returns the current thinking level string.
// Handler returns: string
type GetThinkingLevel struct{ SessionID string }

// GetSessionState returns the current session state (idle/running/permission/ask).
// Handler returns: string
type GetSessionState struct{ SessionID string }

// GetContextUsage returns the context window usage as a percentage (0-100).
// Handler returns: int
type GetContextUsage struct{ SessionID string }

// GetTasks returns the current task list.
// Handler returns: []tasks.Task
type GetTasks struct{ SessionID string }

// GetPlanMode returns the current plan mode and plan file path.
// Handler returns: PlanModeInfo
type GetPlanMode struct{ SessionID string }

// PlanModeInfo is the result of GetPlanMode.
type PlanModeInfo struct {
	Mode                string
	PlanFile            string
	ReviewModelID       string // model ID for plan review
	ReviewModelName     string // display name of review model
	ReviewThinkingLevel string // thinking level for plan review
}

// GetCompactionEpoch returns the current compaction epoch counter.
// Handler returns: int
type GetCompactionEpoch struct{ SessionID string }

// GetPermissionMode returns the current permission mode (yolo/ask/auto).
// Handler returns: string
type GetPermissionMode struct{ SessionID string }

// GetPathPolicy returns the current path policy state.
// Handler returns: PathPolicyInfo
type GetPathPolicy struct{ SessionID string }

// PathPolicyInfo is the result of GetPathPolicy.
type PathPolicyInfo struct {
	WorkspaceRoot string
	Scope         string
	AllowedPaths  []string
}

// GetPermissionInfo returns detailed permission info (mode, patterns, rules).
// Handler returns: PermissionInfo
type GetPermissionInfo struct{ SessionID string }

// PermissionInfo is the result of GetPermissionInfo.
type PermissionInfo struct {
	Mode          string
	AllowPatterns []string
	Rules         []string
}

// GetSessionError returns the last error message from the state machine.
// Handler returns: string
type GetSessionError struct{ SessionID string }

// GetPendingApproval returns pending permission/ask info for WS init data.
// Handler returns: PendingApprovalInfo
type GetPendingApproval struct{ SessionID string }
