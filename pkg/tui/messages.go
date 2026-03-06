package tui

import "github.com/ealeixandre/moa/pkg/core"

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

// flushDoneMsg signals that tea.Println has been processed and it's safe
// to advance flushedCount. Prevents the visual flash where View() renders
// empty because flushedCount was advanced before println executed.
type flushDoneMsg struct {
	upTo  int // advance flushedCount to this value
	epoch int // must match current flushEpoch (stale after /clear)
}
