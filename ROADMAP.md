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

### Providers

- [x] **OpenAI provider** — Full provider with streaming, tool calls, and OAuth (ChatGPT subscription). Supports GPT-4.1 / o3 / GPT-5.3 Codex.
- [ ] **Cost tracking** — Track tokens and estimated cost per session. Show on exit or on demand.

### Model & thinking switching

- [x] **Runtime model switch** — `/model <spec>` or picker. Cross-provider switches (Anthropic ↔ OpenAI) with provider factory. Transient status in View(), committed to session on next send.
- [x] **Thinking level switch** — `/thinking <level>` to change thinking budget at runtime.

### Tool execution

- [ ] **Parallel tool calls** — When the LLM returns multiple tool_use blocks, execute independent calls concurrently. Go makes this natural (goroutines + WaitGroup). TUI needs to show N tools running at once.

### MCP

- [ ] **MCP client** — Model Context Protocol for external tool servers. Core feature, not extension. Must be lightweight: zero cost when no servers are configured. JSON-RPC over stdio. Discover tools → register in tool registry → route calls.

---

## Later / Ideas

- More providers (Gemini, Ollama/local)
- Git awareness (auto-detect repo, show diffs, suggest commits)
- Custom tools (load from config without recompiling)
- Web search / fetch as built-in tools

---

## Non-goals

- Web UI
- Multi-agent orchestration
- Plugin marketplace
- IDE integration
- Tool confirmation (the agent is YOLO by design — implement via extension if wanted)
