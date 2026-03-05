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

## V1 — Interactive Agent

Make it actually usable as a daily coding tool.

### P0 — Must have

- [ ] **Conversation mode** — REPL loop. User types, agent responds, repeat. Multi-turn with history. `moa` with no `-p` enters interactive mode.
- [ ] **Tool confirmation** — Dangerous tools (bash, write, edit) require user approval before execution. `--yolo` flag to skip.
- [ ] **System prompt engineering** — A real, detailed system prompt tuned for coding tasks. The current one is a placeholder.
- [ ] **Context management** — Token counting (tiktoken or estimate), automatic truncation/summarization when approaching context window limits.
- [ ] **Session persistence** — Save/resume conversations. `moa --resume` to continue where you left off.

### P1 — Should have

- [ ] **TUI** — Markdown rendering, syntax highlighting, spinners, tool output formatting. Bubble Tea or similar.
- [ ] **Streaming tool output** — Show bash output as it runs, not after completion.
- [ ] **Cost tracking** — Track tokens and estimated cost per session. Show on exit.
- [ ] **Compact/summarize** — When context gets long, summarize older turns to free space.
- [ ] **File context** — Auto-include relevant files in context (e.g., files mentioned in errors, recently edited).

### P2 — Nice to have

- [ ] **More providers** — OpenAI (GPT-4.1), Google (Gemini), local (Ollama). Provider interface already supports this.
- [ ] **MCP support** — Model Context Protocol for external tool servers.
- [ ] **Git awareness** — Auto-detect repo, show diffs, suggest commits.
- [ ] **Parallel tool execution** — Run independent tool calls concurrently.
- [ ] **Custom tools** — Load tools from config/extensions without recompiling.

---

## Decisions to make

- **TUI library**: Bubble Tea vs Lip Gloss vs raw ANSI? Bubble Tea is the Go standard for TUIs.
- **Token counting**: Use tiktoken-go or estimate by chars? tiktoken-go adds a dependency but is accurate.
- **Session format**: JSON file per session? SQLite? Flat files in `~/.config/moa/sessions/`?
- **Config file**: YAML/TOML/JSON for model defaults, tool permissions, etc.?

---

## Non-goals (V1)

- Web UI
- Multi-agent orchestration
- Plugin marketplace
- Language server / IDE integration
