# Overview

Moa is a coding agent runtime in Go designed to stay lightweight while being useful for real daily work.

## Interfaces

Moa currently has three ways to use the same agent core:

- **Headless CLI** — one-shot prompts and scripts
- **Interactive TUI** — terminal-first chat interface
- **Web UI** via `moa serve` — browser sessions, useful across devices

## Core capabilities

- Tool calling with filesystem sandboxing
- Permission modes: `yolo`, `ask`, `auto`
- Session persistence and resume
- Subagents, including async background jobs
- Automatic context compaction
- MCP tool servers
- Multi-provider support (Anthropic + OpenAI)

## Runtime model

At a high level:

1. The user sends a prompt.
2. The provider streams assistant output.
3. Tool calls are validated and executed.
4. Tool results go back to the model.
5. The loop continues until the assistant stops without new tool calls.

That same loop powers CLI, TUI, and `moa serve`.

## Storage paths

Default paths in code:

- credentials: `~/.config/moa/auth.json`
- sessions: `~/.config/moa/sessions/`
- config: `~/.config/moa/config.json`

## Related docs

- [Quickstart](./quickstart.md)
- [CLI Reference](./cli.md)
- [Serve / Web UI](./serve.md)
- [Configuration](./configuration.md)
- [Architecture](./architecture.md)
