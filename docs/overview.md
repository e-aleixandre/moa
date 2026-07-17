# Overview

Moa is a coding agent runtime in Go. One core, three interfaces: TUI, web UI, and headless CLI.

## What it does

- **Tool calling** with filesystem sandboxing and path policies
- **Permissions**: `yolo`, `ask`, or `auto` (AI-evaluated) modes
- **Plan mode**: plan-then-execute workflow with task tracking
- **Goal mode**: autonomous maker→verifier loop that works toward an objective until a read-only verifier judges it done
- **Sessions**: persist, resume, browse previous conversations
- **Subagents**: spawn child agents, sync or async
- **Memory**: cross-session persistent project notes
- **Skills**: loadable knowledge packs discovered from `.moa/skills/` or `~/.config/moa/skills/`, pulled in on demand via the `load_skill` tool
- **Budget & limits**: per-run USD caps, turn limits, duration limits
- **Checkpoint / undo**: revert file changes per agent turn
- **Context compaction**: automatic summarization when context grows large
- **MCP**: connect external tool servers
- **Voice input**: in both TUI and web UI
- **Prompt templates**: reusable prompts from `~/.config/moa/prompts/` or `.moa/prompts/`
- **AGENTS.md**: project instructions discovered automatically from working directory
- **Multi-provider**: Anthropic and OpenAI, with model aliases for quick switching

## How it works

1. User sends a prompt.
2. Provider streams assistant output.
3. Tool calls are validated, permission-checked, and executed.
4. Tool results go back to the model.
5. Loop continues until the assistant stops calling tools.

That same loop powers all three interfaces.

## Storage

All state lives under `~/.config/moa/`:

| Path | Contents |
|------|----------|
| `config.json` | Global config |
| `auth.json` | Provider credentials |
| `sessions/` | Saved sessions |
| `prompts/` | Global prompt templates |
| `skills/` | Global skill packs (`<name>/SKILL.md`) |
| `global/memory/` | Global memory facts (user, feedback) |
| `projects/<hash>/memory/` | Per-workspace project memory facts |
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
