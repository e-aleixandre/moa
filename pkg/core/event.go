package core

// AgentEvent is emitted by the agent loop for UI/extension consumption.
type AgentEvent struct {
	Type string

	// Populated per type:
	Message        AgentMessage       // message_start, message_end, user_message
	AssistantEvent *AssistantEvent    // message_update (streaming deltas)
	Text           string             // steer, user_message (plain-text prompt)
	SteerID        string             // steer
	MsgID          string             // steer, user_message (MsgID of the user message, for client dedup)
	AttachmentIDs  []string           // steers_canceled
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
	// AgentEventUserMessage reports a user prompt that just entered the
	// conversation as the first message of a new run, emitted at the append
	// point (under the state lock) so the fact is already true in history when
	// subscribers see it. Mid-run injections keep using AgentEventSteer.
	AgentEventUserMessage = "user_message"
	// AgentEventSteersCanceled reports queued steers dropped after a failed run.
	AgentEventSteersCanceled = "steers_canceled"

	AgentEventCompactionStart = "compaction_start"
	AgentEventCompactionEnd   = "compaction_end"
	// AgentEventFastUnavailable reports that a provider fell back from a
	// rejected premium-speed request and disabled it for the session.
	AgentEventFastUnavailable = "fast_unavailable"
)
