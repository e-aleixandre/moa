package tui

import (
	"github.com/ealeixandre/moa/pkg/core"
	"github.com/ealeixandre/moa/pkg/permission"
	"github.com/ealeixandre/moa/pkg/session"
)

// agentEventMsg wraps an agent event for the TUI event loop.
// RunGen scopes the event to a specific run — late events from previous runs are ignored.
type agentEventMsg struct {
	Event  core.AgentEvent
	RunGen uint64
}

// agentDoneMsg signals the event channel is closed or the TUI is quitting.
type agentDoneMsg struct{}

// agentRunResultMsg carries the result of agent.Send().
// Messages is the source-of-truth conversation history for reconciliation.
type agentRunResultMsg struct {
	Err      error
	Messages []core.AgentMessage
	RunGen   uint64
}

// renderTickMsg triggers a stream cache refresh during streaming (~60fps).
type renderTickMsg struct{}

// clearThinkingStatusMsg clears the ephemeral Ctrl+T toggle feedback.
type clearThinkingStatusMsg struct{}

// sessionSavedMsg signals an async session save completed.
type sessionSavedMsg struct{ err error }

// pinnedModelsSavedMsg signals an async pinned-models save completed.
type pinnedModelsSavedMsg struct{ err error }

// compactResultMsg carries the result of a manual /compact command.
type compactResultMsg struct {
	Payload *core.CompactionPayload
	Err     error
}

// permissionRequestMsg carries a tool permission request from the agent loop.
type permissionRequestMsg struct {
	Request permission.Request
}

// sessionBrowserLoadedMsg carries the session list shown by --resume.
type sessionBrowserLoadedMsg struct {
	Summaries []session.Summary
	Err       error
}

// sessionPreviewLoadedMsg carries the preview for the currently highlighted session.
type sessionPreviewLoadedMsg struct {
	ID      string
	Session *session.Session
	Err     error
}

// sessionOpenLoadedMsg carries the session chosen in the browser.
type sessionOpenLoadedMsg struct {
	Session *session.Session
	Err     error
}

// asyncSubagentCountMsg carries the current number of running async subagents.
type asyncSubagentCountMsg struct{ count int }

// SubagentNotification is the structured payload for an async subagent completion.
// Sent from main.go to the TUI via a channel, avoiding fragile text parsing.
type SubagentNotification struct {
	JobID      string // job identifier
	Task       string // original task description
	Status     string // "completed", "failed", or "cancelled"
	AgentText  string // full text for the agent (up to 50 lines of result)
	ResultTail string // result tail for TUI preview
}

// subagentNotifyMsg wraps a SubagentNotification for the Bubble Tea event loop.
type subagentNotifyMsg struct{ notification SubagentNotification }
