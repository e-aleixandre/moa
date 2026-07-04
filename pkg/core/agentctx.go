package core

import "context"

// agentIDKey is the private context key under which the agent identifier is
// stored. Using a struct type avoids collisions with other context values.
type agentIDKey struct{}

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
