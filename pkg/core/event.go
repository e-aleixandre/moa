package core

// AgentEvent is emitted by the agent loop for UI/extension consumption.
type AgentEvent struct {
	Type string

	// Populated per type:
	Message        AgentMessage    // message_start, message_end
	AssistantEvent *AssistantEvent // message_update (streaming deltas)
	ToolCallID     string          // tool_execution_*
	ToolName       string          // tool_execution_*
	Args           map[string]any  // tool_execution_start
	Result         *Result         // tool_execution_end/update
	IsError        bool            // tool_execution_end
	Messages       []AgentMessage  // agent_end (full conversation)
	Error          error           // agent_error
}

// Agent event type constants.
const (
	AgentEventStart          = "agent_start"
	AgentEventEnd            = "agent_end"
	AgentEventError          = "agent_error"
	AgentEventTurnStart      = "turn_start"
	AgentEventTurnEnd        = "turn_end"
	AgentEventMessageStart   = "message_start"
	AgentEventMessageUpdate  = "message_update"
	AgentEventMessageEnd     = "message_end"
	AgentEventToolExecStart  = "tool_execution_start"
	AgentEventToolExecUpdate = "tool_execution_update"
	AgentEventToolExecEnd    = "tool_execution_end"
)
