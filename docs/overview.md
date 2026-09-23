# Overview

Moa is a coding agent runtime in Go. One core, two interfaces: web UI and headless CLI.

## What it does

- **Tool calling** with filesystem sandboxing and path policies
- **Permissions**: `yolo`, `ask`, or `auto` (AI-evaluated) modes
- **Goal mode**: autonomous maker→verifier loop that works toward an objective until a read-only verifier judges it done
- **Sessions**: persist, resume, browse previous conversations
- **Subagents**: spawn child agents, sync or async
- **Memory**: cross-session persistent project notes
- **Skills**: loadable knowledge packs discovered from `.moa/skills/` or `~/.config/moa/skills/`. The agent pulls one in with `load_skill`; you can invoke one yourself with `/<name>`. `context: fork` runs the skill as an isolated subagent instead of loading it into the current conversation
- **Budget & limits**: per-run USD caps, turn limits, duration limits
- **Checkpoint / undo**: revert file changes per agent turn
- **Context compaction**: when context grows large, old tool outputs are trimmed first (a line in the transcript says how much was freed) and the conversation is summarized only when that is not enough — by the session's model or by a [summarizer you choose](./configuration.md#features)
- **MCP**: connect external tool servers, local or remote; remote servers that need a login sign in with OAuth from the session's MCP page
- **Mid-run messages**: what you send while the agent works is [queued](./serve.md#queued-commands-and-mid-run-messages) and read at its next step; one tap brings the whole queue back to the input
- **[Artifacts](./serve.md#files-sent-by-the-agent)**: files the agent sends you stay reachable from the conversation
- **Voice input**: dictate messages in the web UI, or hand the conversation to a voice delegate and get its minutes back as a draft
- **[Live Preview](./serve.md#live-preview)**: watch the web app the agent is building inside the conversation, at a chosen viewport width, and tap an element to tell the agent what should change about it
- **[Wake on event](./automation.md#event-hooks)**: give an external system (CI, error tracker, mail watcher) its own webhook URL and let what it sends reach a session, or wait in an inbox for you to place it
- **[Project owners](./owners.md)**: a standing agent per codebase that keeps the project's book, starts and directs its sessions, receives their reports and answers what the book already answers
- **AGENTS.md**: project instructions discovered automatically from working directory; `/reload` re-reads them in an open session
- **Multi-provider**: Anthropic, OpenAI, xAI Grok and Meta (Muse Spark), with model aliases for quick switching, per-model [thinking levels](./cli.md#thinking-levels) and an optional [fast mode](./cli.md#fast-mode)

## How it works

1. User sends a prompt.
2. Provider streams assistant output.
3. Tool calls are validated, permission-checked, and executed.
4. Tool results go back to the model.
5. Loop continues until the assistant stops calling tools.

That same loop powers both interfaces.

## Storage

All state lives under `~/.config/moa/` (or `MOA_CONFIG_DIR`):

| Path | Contents |
|------|----------|
| `config.json` | Global config |
| `auth.json` | Provider credentials (mode `0600`) |
| `sessions/` | Saved sessions |
| `schedules.json` | Durable one-shot schedules (`/schedule`) |
| `projects/<hash>/state.json` | [Your project state](./configuration.md#your-project-state): saved approvals, MCP vetoes, your own per-project settings |
| `attachments/v1/` | Image and document bytes referenced by sessions, deduplicated by content |
| `skills/` | Global skill packs (`<name>/SKILL.md`) |
| `global/memory/` | Global memory facts (scope: global) |
| `codebases/<key>/memory/` | Project memory facts, keyed by repository |
| `codebases/<key>/book/`, `owner.json` | A [project owner](./owners.md)'s book and settings |
| `codebases/orphaned-memory.json` | Older project memory that no repository could claim |
| `.mcp.json` | Global MCP server definitions |
| `mcp-oauth.json` | Sign-in tokens for remote MCP servers |
| `events.json` | Event inbox for [wake on event](./automation.md#event-hooks) |
| `devices.json` | Paired device credentials |
| `update.json` | Cached release-check state |
| `vapid.json` | Web-push VAPID keypair |
| `push_subscriptions.json` | Web-push subscriptions |

Project-level config goes in `<cwd>/.moa/` — see [Configuration](./configuration.md).

## Next

- [Quickstart](./quickstart.md) — get running in 2 minutes
- [Configuration](./configuration.md) — all config options
- [Tools](./tools.md) — what the agent can do
- [Web UI](./serve.md) — `moa serve`, skills and security
- [Architecture](./architecture.md) — how it's built
- [Releases](./releases.md) — release conventions and checklist
