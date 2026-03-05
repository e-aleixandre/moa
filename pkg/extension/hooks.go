package extension

import (
	"context"

	"github.com/ealeixandre/go-agent/pkg/core"
)

// --- Hook handler types (each has appropriate return type) ---

// ObserverHook: fire-and-forget. Return value ignored. Async, no deadline.
type ObserverHook func(ctx context.Context, event core.AgentEvent)

// BeforeAgentStartHook: can inject messages before the first LLM call.
// Blocking, 200ms deadline.
type BeforeAgentStartHook func(ctx context.Context) (inject []core.AgentMessage, err error)

// ToolCallHook: can block a tool call before execution.
// Blocking, 200ms deadline.
type ToolCallHook func(ctx context.Context, toolName string, args map[string]any) *core.ToolCallDecision

// ToolResultHook: can observe or modify a tool result after execution.
// Blocking, 200ms deadline.
type ToolResultHook func(ctx context.Context, toolName string, result core.Result, isError bool) (*core.Result, error)

// ContextHook: can modify the message list before convertToLLM.
// Blocking, 500ms deadline (context manipulation may be heavier).
type ContextHook func(ctx context.Context, messages []core.AgentMessage) ([]core.AgentMessage, error)
