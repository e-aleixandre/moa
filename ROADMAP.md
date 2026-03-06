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

- [ ] **Token counting** — Track token usage per turn. Tiktoken-go or provider-reported counts.
- [ ] **Context window management** — Detect when approaching limit, warn or auto-act.
- [ ] **Compact / summarize** — When context grows too long, summarize older turns to free space. Keep recent turns intact. The LLM does the summarization.

### Providers

- [ ] **OpenAI provider** — GPT-4.1 / o3. Provider interface already supports this, mainly mapping to their API format.
- [ ] **Cost tracking** — Track tokens and estimated cost per session. Show on exit or on demand.

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
