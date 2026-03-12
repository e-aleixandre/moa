# Architecture

## Package map

```text
cmd/agent/          CLI entrypoint and runtime wiring
pkg/agent/          Core loop, state, events, steering, run lifecycle
pkg/auth/           Credential store + OAuth flows
pkg/compaction/     Context summarization and cut-point logic
pkg/context/        AGENTS.md discovery + system prompt builder
pkg/core/           Shared provider/message/tool/config abstractions
pkg/extension/      Extension host + typed hooks
pkg/mcp/            MCP manager and stdio tool-server integration
pkg/permission/     Gate for yolo/ask/auto tool approvals
pkg/provider/       Provider factory and adapters
pkg/provider/anthropic/
pkg/provider/openai/
pkg/serve/          HTTP/WebSocket server and web UI session manager
pkg/session/        Session persistence (file-backed)
pkg/subagent/       Child-agent execution and async jobs
pkg/tool/           Built-in tools + validation
pkg/tui/            Bubble Tea application and components
```

## Execution model

The same agent core is reused across all interfaces:

- **CLI** calls the agent directly
- **TUI** wraps the agent in a terminal application
- **Serve** wraps the agent in HTTP/WebSocket session management

## Agent loop

At a high level:

1. Fire lifecycle hooks.
2. Optionally compact context.
3. Build the provider request.
4. Stream assistant events.
5. Extract tool calls.
6. Validate and permission-check them.
7. Execute approved tools.
8. Append tool results and continue.
9. End when the assistant stops without more tool calls.

## Event model

The runtime emits typed `core.AgentEvent` values for:

- message streaming deltas
- tool execution start/update/end
- turn and run boundaries
- compaction lifecycle
- steering injection

TUI and `pkg/serve` consume those events differently, but both rely on the same underlying stream.

## Sessions

Sessions persist full `[]core.AgentMessage` plus metadata and compaction epoch using atomic file writes.

- TUI uses this for resume and session browser
- `moa serve` uses it for saved web sessions and resume

## Design constraints

- The agent loop depends on a hook interface, not a concrete extension host.
- Errors are returned, not panics.
- `pkg/core` stays dependency-light.
- Interface layers should reuse the core runtime instead of re-implementing it.
