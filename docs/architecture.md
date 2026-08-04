# Architecture

## Package map

### Core runtime

| Package | Role |
|---------|------|
| `cmd/moa/` | CLI entrypoint, flag parsing, runtime wiring |
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
| `pkg/goal/` | Autonomous maker→verifier loop toward an objective (`/goal`). The verifier is a read-only mini-agent (read/grep/find/ls) that inspects the repo to judge completion, and carries memory of its earlier verdicts across iterations; `--verify-oneshot` falls back to the legacy tool-less check. When the project defines `.moa/verify.json`, red checks are a hard gate — the goal can't be declared done while they fail, whatever the verdict says |
| `pkg/autotitle/` | Generates short session titles from the conversation via a cheap LLM call |
| `pkg/pulsebrief/` | Generates a per-session status brief (attempting/progress) via a cheap same-vendor LLM call; feeds the web dashboard and Pulse voice (web/Pulse-only) |

### Providers

| Package | Role |
|---------|------|
| `pkg/provider/` | Factory: resolve model → provider adapter |
| `pkg/provider/anthropic/` | Anthropic Messages API (streaming SSE) |
| `pkg/provider/openai/` | OpenAI API transport and transcription |
| `pkg/provider/responses/` | Shared stateless Responses request/replay codec for OpenAI-compatible Responses transports (`store: false`) |
| `pkg/provider/xai/` | xAI Responses transport: `api.x.ai` API keys or the separate Grok consumer OAuth proxy |
| `pkg/provider/retry/` | Retry wrapper with backoff |
| `pkg/provider/sseutil/` | SSE timeout reader |

### Interfaces

| Package | Role |
|---------|------|
| `pkg/tui/` | Bubble Tea terminal application |
| `pkg/serve/` | HTTP/WebSocket server + web UI session manager; also hosts the Pulse backend (device pairing, guardian WebSocket, Realtime client-secret broker, session brief) |

### Infrastructure

| Package | Role |
|---------|------|
| `pkg/auth/` | Credential store + OAuth flows, including xAI's OIDC device grant |
| `pkg/session/` | Session persistence (file-backed, atomic writes) |
| `pkg/extension/` | Extension host + typed hooks (internal; fired every turn but no user-facing loader/config to register extensions yet) |
| `pkg/mcp/` | MCP manager — stdio tool-server integration |
| `pkg/git/` | Git context detection |
| `pkg/clipboard/` | Clipboard integration (platform-specific) |
| `pkg/files/` | File utilities |
| `pkg/jsonutil/` | JSON parsing utilities |
| `pkg/push/` | Web Push notifications (VAPID keys, subscription store, dispatch) |
| `pkg/usage/` | Provider-qualified plan-usage pollers; Claude and xAI consumer data come from private, best-effort endpoints |
| `pkg/attention/` | Attention Service: consumes every session's event bus and produces a priority-ordered attention queue |
| `pkg/schedule/` | Durable one-shot schedule records (backs the web `/schedule` command) |
| `pkg/release/` | Build metadata and best-effort release update checks |
| `pkg/ansi/` | Strips terminal control sequences from untrusted text before rendering |

## Execution model

The same agent core is reused across all interfaces:

- **CLI** calls the agent directly, streams events to stdout
- **TUI** wraps the agent in a Bubble Tea terminal app
- **Serve** wraps the agent in HTTP/WebSocket session management

## Event bus

`pkg/bus` is the central nervous system. Components communicate through typed messages:

- **Events** — async, fan-out (e.g. `ToolExecStarted`, `PlanModeChanged`)
- **Commands** — sync, one handler (e.g. `EnterPlanMode`, `AbortRun`)
- **Queries** — sync, request-response (e.g. `GetPlanMode`, `GetSessionState`)

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

## Compaction

When a session approaches its context window it is summarized: an older prefix
of the conversation is replaced by a single summary message, and a recent tail
is kept verbatim. `FindCutPoint` picks the boundary, `SerializeForSummary`
renders the prefix for the summarizer, and `Compact` performs the swap.

What survives compaction is whatever the summary records, so the design goal is
not compression but **retention of what cannot be recovered by other means**.
File contents, command output and code can be read again; decisions, rejected
approaches, constraints and user intent cannot. Three properties follow:

- **Recency is protected.** On overflow the serializer drops the *oldest*
  messages, never the newest, and never the final message — an empty transcript
  would let a summary of nothing replace real history.
- **Tool results keep their ending.** Their budget is spent newest-first, and
  anything that must be shortened keeps its head *and* its tail: the outcome of
  a tool call (the failing assertion, the final error) lives at the end. Tool
  output is capped at a share of the transcript so it cannot evict the dialogue,
  which is the part carrying intent.
- **The output budget is explicit.** The summary has a declared token budget
  that scales with the context window, and the cut point reserves that same
  figure so the two cannot drift apart. Thinking is disabled for the call, since
  it draws from the same output budget.

The **session checkpoint** (`pkg/sessioncheckpoint`) is an escape hatch from the
summarizer: a small slot the model can write before compaction, appended to the
summary mechanically so it cannot be omitted or paraphrased. Both the manual and
automatic paths append it. It is ephemeral — consumed once, cleared by
generation so a checkpoint written mid-compaction is not lost, and never
consumed by a failed or cancelled compaction.

Compaction is lossy by construction. The full transcript remains on disk: the
session log is append-only, so a summarized session can still be inspected in
full after the fact.

## Design constraints

- The agent loop depends on a hook interface, not a concrete extension host.
- Errors are returned, not panics.
- `pkg/core` stays dependency-light.
- Interface layers reuse the core runtime — they don't reimplement it.
