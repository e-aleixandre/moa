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
- **Context compaction**: automatic summarization when context grows large
- **MCP**: connect external tool servers
- **Voice input**: in the web UI
- **AGENTS.md**: project instructions discovered automatically from working directory; `/reload` re-reads them in an open session
- **Multi-provider**: Anthropic, OpenAI, and xAI Grok, with model aliases for quick switching

## How it works

1. User sends a prompt.
2. Provider streams assistant output.
3. Tool calls are validated, permission-checked, and executed.
4. Tool results go back to the model.
5. Loop continues until the assistant stops calling tools.

That same loop powers both interfaces.

## Storage

All state lives under `~/.config/moa/`:

| Path | Contents |
|------|----------|
| `config.json` | Global config |
| `auth.json` | Provider credentials |
| `sessions/` | Saved sessions |
| `attachments/v1/` | Image and document bytes referenced by sessions, deduplicated by content |
| `skills/` | Global skill packs (`<name>/SKILL.md`) |
| `global/memory/` | Global memory facts (scope: global) |
| `codebases/<key>/memory/` | Project memory facts, keyed by repository |
| `codebases/orphaned-memory.json` | Older project memory that no repository could claim |
| `.mcp.json` | Global MCP server definitions |
| `devices.json` | Paired Pulse device credentials |
| `update.json` | Cached release-check state |
| `vapid.json` | Web-push VAPID keypair |
| `push_subscriptions.json` | Web-push subscriptions |

Project-level config goes in `<cwd>/.moa/` — see [Configuration](./configuration.md).

## Next

- [Quickstart](./quickstart.md) — get running in 2 minutes
- [Configuration](./configuration.md) — all config options
- [Architecture](./architecture.md) — how it's built
- [Releases](./releases.md) — release conventions and checklist
