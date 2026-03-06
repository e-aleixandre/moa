package extension

import (
	"log/slog"

	"github.com/ealeixandre/moa/pkg/core"
)

// Extension is a Go-native extension that registers hooks and tools.
type Extension interface {
	// Init is called once when the extension is loaded.
	// Use api to register hooks, tools, and commands.
	Init(api API) error
}

// API is what extensions receive to interact with the agent.
type API interface {
	// Typed hook registration
	OnBeforeAgentStart(fn BeforeAgentStartHook)
	OnAgentEnd(fn ObserverHook)
	OnTurnStart(fn ObserverHook)
	OnTurnEnd(fn ObserverHook)
	OnToolCall(fn ToolCallHook)
	OnToolResult(fn ToolResultHook)
	OnContext(fn ContextHook)

	// Tool registration
	RegisterTool(t core.Tool)
	UnregisterTool(name string)

	// Logging
	Logger() *slog.Logger
}
