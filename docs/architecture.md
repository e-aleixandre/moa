# Architecture

## Package map

### Core runtime

| Package | Role |
|---------|------|
| `cmd/agent/` | CLI entrypoint, flag parsing, runtime wiring |
| `pkg/agent/` | Agent loop, state machine, steering, budget enforcement |
| `pkg/bus/` | Typed event bus — commands, queries, events between components |
| `pkg/core/` | Shared abstractions: provider, message, tool, config, models |
| `pkg/bootstrap/` | Runtime config assembly and startup helpers |

### Agent capabilities

| Package | Role |
|---------|------|
| `pkg/tool/` | Built-in tools (bash, read, write, edit, grep, find, etc.) |
| `pkg/compaction/` | Context summarization when token window fills up |
| `pkg/context/` | System prompt builder + `AGENTS.md` discovery |
| `pkg/permission/` | Gate for `yolo`/`ask`/`auto` tool approvals |
| `pkg/checkpoint/` | File-level undo — snapshots before writes, pop to revert |
| `pkg/memory/` | Cross-session persistent project memory |
| `pkg/planmode/` | Plan-then-execute workflow, tool restrictions, task tracking |
| `pkg/tasks/` | Task store and tool for plan execution |
| `pkg/subagent/` | Child agent execution and async job management |
| `pkg/verify/` | Run project verification checks |
| `pkg/skill/` | Skill file loading |
| `pkg/prompt/` | Prompt template discovery and loading |
| `pkg/askuser/` | `ask_user` tool bridge to UI |

### Providers

| Package | Role |
|---------|------|
| `pkg/provider/` | Factory: resolve model → provider adapter |
| `pkg/provider/anthropic/` | Anthropic Messages API (streaming SSE) |
| `pkg/provider/openai/` | OpenAI Chat API (streaming + transcription) |
| `pkg/provider/retry/` | Retry wrapper with backoff |
| `pkg/provider/sseutil/` | SSE timeout reader |

### Interfaces

| Package | Role |
|---------|------|
| `pkg/tui/` | Bubble Tea terminal application |
| `pkg/serve/` | HTTP/WebSocket server + web UI session manager |

### Infrastructure

| Package | Role |
|---------|------|
| `pkg/auth/` | Credential store + OAuth flows |
| `pkg/session/` | Session persistence (file-backed, atomic writes) |
| `pkg/extension/` | Extension host + typed hooks |
| `pkg/mcp/` | MCP manager — stdio tool-server integration |
| `pkg/git/` | Git context detection |
| `pkg/clipboard/` | Clipboard integration (platform-specific) |
| `pkg/files/` | File utilities |
| `pkg/jsonutil/` | JSON parsing utilities |

## Execution model

The same agent core is reused across all interfaces:

- **CLI** calls the agent directly, streams events to stdout
- **TUI** wraps the agent in a Bubble Tea terminal app
- **Serve** wraps the agent in HTTP/WebSocket session management

## Event bus

`pkg/bus` is the central nervous system. Components communicate through typed messages:

- **Events** — async, fan-out (e.g. `ToolStarted`, `PlanModeChanged`)
- **Commands** — sync, one handler (e.g. `EnterPlanMode`, `CancelRun`)
- **Queries** — sync, request-response (e.g. `GetPlanMode`, `GetSessionInfo`)

The TUI and serve layer subscribe to events for rendering. The agent loop publishes events and handles commands.

## Agent loop

1. Fire lifecycle hooks.
2. Check budget and turn limits.
3. Optionally compact context.
4. Build the provider request.
5. Stream assistant events.
6. Extract tool calls.
7. Validate and permission-check them.
8. Execute approved tools (checkpoint files first).
9. Append tool results and continue.
10. End when the assistant stops without more tool calls.

## Sessions

Sessions persist full message history plus metadata using atomic file writes. Both TUI and serve use the same session store for persistence and resume.

## Design constraints

- The agent loop depends on a hook interface, not a concrete extension host.
- Errors are returned, not panics.
- `pkg/core` stays dependency-light.
- Interface layers reuse the core runtime — they don't reimplement it.
