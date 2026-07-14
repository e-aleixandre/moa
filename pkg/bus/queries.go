package bus

import "github.com/ealeixandre/moa/pkg/core"

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

// GetSessionCost returns the accumulated session cost in USD (main run + subagents).
// Handler returns: float64
type GetSessionCost struct{ SessionID string }

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

// GetGoal returns the current goal-mode state.
// Handler returns: GoalInfo
type GetGoal struct{ SessionID string }

// GoalInfo is the result of GetGoal.
type GoalInfo struct {
	Active        bool
	Objective     string
	WorkDir       string
	Iteration     int
	Stalled       int
	MaxIterations int
	MaxStalled    int
	Verifying     bool // a verifier run is currently in flight
}

// GetCompactionEpoch returns the current compaction epoch counter.
// Handler returns: int
type GetCompactionEpoch struct{ SessionID string }

// GetCompacting reports whether a compaction is currently in progress, so a
// reconnect snapshot can restore (or clear) the compacting spinner.
// Handler returns: bool
type GetCompacting struct{ SessionID string }

// StreamingAggregate is the in-flight partial assistant text/thinking and its
// message ID, surfaced in the reconnect snapshot so a reconnect during
// generation restores the whole streamed-so-far reply instead of only post-cut
// deltas. Captured atomically with the sequence cut via
// SessionContext.SnapshotStreamingWithCut. Empty Text and Thinking mean nothing
// is streaming right now.
type StreamingAggregate struct {
	Text     string
	Thinking string
	MsgID    string
}

// GetPendingSteers returns the authoritative queue of steer messages not yet
// delivered, so a reconnect snapshot can restore the queued-message chips.
// Handler returns: []core.SteerItem
type GetPendingSteers struct{ SessionID string }

// GetQueueLen returns the number of items in the unified queue rail (steers and
// command barriers not yet delivered/executed). The serve layer uses it to
// decide whether a /send starts a run directly (idle and empty queue) or must
// be enqueued as a steer to preserve strict send order.
// Handler returns: int
type GetQueueLen struct{ SessionID string }

// GetUndeliveredNativeBytes returns the decoded native document/image bytes that
// are accepted into the session (queued steers plus any drained batch in flight
// to history) but not yet visible in history. The serve quota check adds it to
// the history total so concurrent sends can't collectively exceed the
// per-session native-content budget through the async delivery window.
// Handler returns: int64
type GetUndeliveredNativeBytes struct{ SessionID string }

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

// GetDisplayMessages returns the full message history for display (from tree).
// Unlike GetMessages, this includes pre-compaction messages.
// Handler returns: []core.AgentMessage
type GetDisplayMessages struct{ SessionID string }

// GetBranchPoints returns branch points for the branch picker UI.
// Handler returns: []BranchPoint
type GetBranchPoints struct{ SessionID string }

// GetSubagents returns a snapshot of currently live subagent jobs (running or
// cancelling), including their accumulated transcript. Used to populate the
// agent tray and reconnect clients mid-run. Bus itself does not know about
// pkg/subagent — the handler is registered by the frontend (serve/TUI) that
// owns the *subagent.Jobs handle.
// Handler returns: []SubagentSnapshot
type GetSubagents struct{ SessionID string }

// SubagentSnapshot describes one live subagent job, including its transcript
// so far. Result element type for GetSubagents.
type SubagentSnapshot struct {
	JobID    string
	Task     string
	Model    string
	Status   string
	Async    bool
	Messages []core.AgentMessage
}

// GetBashJobs returns active/recent background bash jobs for reconnecting UIs.
type GetBashJobs struct{ SessionID string }

// BashJobSnapshot is the transport-safe snapshot of one background bash job.
type BashJobSnapshot struct {
	JobID   string
	Command string
	CWD     string
	Status  string
	Output  string
}

// BranchPoint describes a possible branch target in the conversation.
type BranchPoint struct {
	EntryID       string `json:"entry_id"`
	Label         string `json:"label"` // first line of message
	Role          string `json:"role"`  // user/assistant
	Timestamp     int64  `json:"timestamp"`
	BranchCount   int    `json:"branch_count"` // number of children
	IsCurrentPath bool   `json:"is_current_path"`
}
