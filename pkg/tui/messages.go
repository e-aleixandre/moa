package tui

import "github.com/ealeixandre/go-agent/pkg/core"

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

// expandDoneMsg signals the pager closed after Ctrl+O expand mode.
type expandDoneMsg struct{ err error }

// clearThinkingStatusMsg clears the ephemeral Ctrl+T toggle feedback.
type clearThinkingStatusMsg struct{}
