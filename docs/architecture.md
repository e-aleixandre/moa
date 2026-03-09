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
pkg/permission/     Gate for yolo/ask/auto tool approvals
pkg/provider/       Provider factory and adapters
pkg/provider/anthropic/
pkg/provider/openai/
pkg/session/        Session persistence (file-backed)
pkg/subagent/       Child-agent execution and async jobs
pkg/tool/           Built-in tools + validation
pkg/tui/            Bubble Tea application and components
```

## Agent loop (simplified)

1. Fire lifecycle hooks (`agent_start`, `turn_start`, etc.).
2. Optionally compact context when threshold is reached.
3. Build provider request (system + messages + tool specs).
4. Stream assistant events.
5. Extract tool calls.
6. Validate + permission-check tool calls.
7. Execute approved tools (parallel execution; ordered result append).
8. Append tool results and continue turn.
9. End when assistant returns without tool calls.

## Event model

The runtime emits typed `core.AgentEvent` events for:

- message streaming deltas
- tool execution start/update/end
- turn and run boundaries
- compaction lifecycle
- steering injection

TUI consumes these asynchronously and reconciles with source-of-truth messages at run end.

## Providers

- Provider interface: `core.Provider`
- Implementations:
  - Anthropic Messages API
  - OpenAI Responses API
- Factory dispatch: `pkg/provider/factory.go`

## Sessions

File-backed store persists complete `[]core.AgentMessage` plus metadata and compaction epoch. Writes are atomic (`tmp -> rename`).

## Extensions

Extensions register hooks and tools through `pkg/extension` API.

Supported hook types include:

- before-agent-start injection
- tool-call decision
- tool-result transform
- context transform
- observer hooks for lifecycle events

## Design constraints in current codebase

- Agent loop depends on a hook interface (not concrete extension host type).
- Errors are returned, not panics.
- `pkg/core` stays dependency-light and project-internal-package free.
