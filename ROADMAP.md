# Moa — Roadmap

## V0 — Headless Agent ✅

Core loop, streaming, tools, extensions, OAuth. Done.

- [x] Agent loop (prompt → LLM → tools → repeat)
- [x] Anthropic provider with SSE streaming
- [x] 7 built-in tools (bash, read, write, edit, grep, find, ls)
- [x] Extension system with typed hooks + rollback
- [x] Event emitter (async, fan-out)
- [x] OAuth PKCE flow (Claude Max)
- [x] CLI headless mode (`moa -p "..."`)
- [x] Hardening: race-safe, lifecycle guarantees, path sandbox, error handling

---

## V1 — Interactive Agent ✅

- [x] Conversation mode (REPL loop, multi-turn)
- [x] TUI with Bubble Tea (markdown rendering, syntax highlighting, streaming)
- [x] Streaming tool output (bash output as it runs)
- [x] Session persistence (save/resume with `--resume`)
- [x] System prompt (adapted from pi agent, tuned for coding)

---

## V2 — Production Agent

Make it robust enough to be a daily driver.

### Context

- [x] **Token counting** — Provider-reported usage (input, output, cache read/write) tracked per assistant message. Estimated context size via `core.EstimateContext`.
- [x] **Context window management** — Status line shows context usage %. Auto-compaction triggers when approaching limit.
- [x] **Compact / summarize** — LLM-driven compaction: old turns summarized, recent kept verbatim. Compaction epoch invalidates stale estimates.
- [x] **`/compact` command** — Manual compaction via `Agent.Compact()`. Serialized with agent runs, disables input during operation, saves session on completion.
- [x] **Prompt caching (Anthropic)** — `cache_control: ephemeral` on last system block, last tool definition, and last user message. Reduces cost ~90% on cache hits, latency ~80%.
- [x] **Cost tracking** — `Pricing` struct per model with $/M token costs. Delta-based accumulation from assistant Usage. Displayed in top bar. Resets on `/clear`.
- [x] **Tool output budget (head+tail)** — `headTailBuffer` keeps first 25KB + last 25KB. Full output spilled to `/tmp/moa-output/` so the model can explore it with read/grep without re-executing. Replaces old head-only `cappedBuffer`.

### Providers

- [x] **OpenAI provider** — Full provider with streaming, tool calls, and OAuth (ChatGPT subscription). Supports GPT-4.1 / o3 / GPT-5.3 Codex.
- [ ] **More providers** — Gemini, Ollama/local models. Gemini for long context, local for privacy.

### Model & thinking switching

- [x] **Runtime model switch** — `/model <spec>` or picker. Cross-provider switches (Anthropic ↔ OpenAI) with provider factory. Transient status in View(), committed to session on next send.
- [x] **Thinking level switch** — `/thinking <level>` to change thinking budget at runtime.

### Tool execution

- [x] **Parallel tool calls** — Multiple tool calls execute concurrently (goroutines + WaitGroup). Three-phase: pre-flight (hooks, validation) → concurrent execution → sequential collect (ordered results). TUI shows "running N tools..." status.
- [x] **Diff visual in edit/write** — The edit tool has before/after content. Emit a ToolExecUpdate with a unified diff (or changed lines). No external dependency needed — `os/exec` with system `diff` or simple internal diffing.
- [x] **Streaming stderr in bash** — `streamReader` currently mixes stdout and stderr. If TUI received typed chunks (stdout vs stderr), stderr could render dimmed/red. Two pipes already exist — just differentiate the partial result.

### Configuration

- [x] **Three-level config** — Global (`~/.moa/config.json`) → project (`<cwd>/.moa/config.json`) → session (`--yolo` flag). Merged with OR for booleans, concatenation for slices.
- [x] **Configurable sandbox** — `disable_sandbox` disables path restrictions (YOLO mode). `allowed_paths` adds directories accessible outside workspace. Works at all three config levels.

### MCP

- [ ] **MCP client** — Model Context Protocol for external tool servers. Core feature, not extension. Must be lightweight: zero cost when no servers are configured. JSON-RPC over stdio. Discover tools → register in tool registry → route calls.

### TUI

- [x] **Theme system** — Separated Theme (colors) from Layout (rendering). Split + Flat layouts, Catppuccin Mocha default. Theme-derived styles throughout.
- [ ] **Session browser** — `session.Store` has `List()` returning Summary (ID, title, date). A `/sessions` command that opens a picker (reuse `pickerModel` pattern) to navigate and resume sessions without leaving the TUI.
- [ ] **Permission policies** — Per-tool approval rules: "read always OK, write asks confirmation, bash asks if contains rm/sudo". Granular control instead of all-or-nothing.

### Agent capabilities

- [ ] **Subagent (lightweight)** — A tool that runs a mini agent loop with its own context, like Claude Code's Agent tool. Uses `agent.Run()` with a subset of tools and a derived prompt. Useful for "investigate this in parallel" without polluting main context. Not multi-agent orchestration — just a concurrent ExecuteFunc.
- [ ] **`/undo` with file snapshots** — `FireToolCall` hook intercepts before execution. Save previous file content before write/edit modifies it. `/undo` reverts the last change on disk. Not git — circular buffer of N snapshots in memory. Hook infrastructure supports this naturally.
- [ ] **Images in context** — `core.Content` can carry image blocks. A `screenshot` tool that captures a screen region and includes it as an image content block. Providers already support vision.
- [ ] **Web access** — Search and fetch tools for when the agent needs to look up docs, APIs, changelogs. Without this, the agent is blind to anything not in the repo.

---

## Later / Ideas

- Git awareness (auto-detect repo, show diffs, suggest commits, auto-checkpoints before changes)
- Custom tools (load from config without recompiling — partially subsumed by MCP)
- Conversation branching / forking

---

## Non-goals

- Web UI
- Multi-agent orchestration
- Plugin marketplace
- IDE integration
