package agent

import "github.com/ealeixandre/moa/pkg/core"

// AgentState holds the mutable state during an agent run.
type AgentState struct {
	Messages []core.AgentMessage
	Model    core.Model
}
