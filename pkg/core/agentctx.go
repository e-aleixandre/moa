package core

import (
	"context"
	"errors"
)

// ErrWaitInterruptedBySteer marks a wait tool whose parent agent received a
// user steer. The job being observed continues in the background; only the
// parent's blocking wait is interrupted so it can process the new message.
var ErrWaitInterruptedBySteer = errors.New("wait interrupted by user steer")

// agentIDKey is the private context key under which the agent identifier is
// stored. Using a struct type avoids collisions with other context values.
type agentIDKey struct{}

// toolCallIDKey is the private context key under which the current tool call
// identifier is stored.
type toolCallIDKey struct{}

// WithAgentID tags ctx with an agent identifier used to isolate per-agent
// shell state (see pkg/tool.BashState). The root/parent agent uses "" (no tag).
func WithAgentID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, agentIDKey{}, id)
}

// AgentIDFromContext returns the agent id set by WithAgentID, or "" if none.
func AgentIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(agentIDKey{}).(string)
	return id
}

// WithToolCallID tags ctx with the identifier of the tool call being executed.
func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDKey{}, id)
}

// ToolCallIDFromContext returns the tool call id set by WithToolCallID, or ""
// if the context does not represent a tool call.
func ToolCallIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(toolCallIDKey{}).(string)
	return id
}
