package core

// AgentEvent is emitted by the agent loop for UI/extension consumption.
type AgentEvent struct {
	Type string

	// Populated per type:
	Message        AgentMessage       // message_start, message_end
	AssistantEvent *AssistantEvent    // message_update (streaming deltas)
	Text           string             // steer
	ToolCallID     string             // tool_execution_*
	ToolName       string             // tool_execution_*
	Args           map[string]any     // tool_execution_start
	Result         *Result            // tool_execution_end/update
	IsError        bool               // tool_execution_end
	Rejected       bool               // tool_execution_end (true only for permission denial)
	Messages       []AgentMessage     // agent_end (full conversation)
	Compaction     *CompactionPayload // compaction_end
	Error          error              // agent_error, compaction_end (non-fatal)
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

	AgentEventSteer = "steer" // a steering message was injected mid-run

	AgentEventCompactionStart = "compaction_start"
	AgentEventCompactionEnd   = "compaction_end"
)
