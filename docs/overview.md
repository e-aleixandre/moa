# Overview

Moa is a coding agent focused on running locally with a small, composable Go codebase.

## Core capabilities

- **Interactive TUI** built with Bubble Tea
- **Headless mode** (`-p`, `@file`, stdin)
- **Tool calling** with sandboxed filesystem + command execution
- **Permission gate** (`yolo`, `ask`, `auto`)
- **Session persistence** with resume browser
- **Subagents** (sync and async background jobs)
- **Automatic context compaction** for long chats
- **Multi-provider support** (Anthropic + OpenAI)

## Runtime model

At a high level:

1. User prompt enters the agent loop.
2. Provider streams assistant deltas.
3. Tool calls are validated and executed.
4. Tool results return to the model.
5. Loop continues until assistant stops without tool calls.

The same core loop powers both TUI and headless modes.

## Storage paths

Current defaults in code:

- credentials: `~/.config/moa/auth.json`
- sessions: `~/.config/moa/sessions/`
- config: `~/.config/moa/config.json`

## Related docs

- [Quickstart](./quickstart.md)
- [Configuration](./configuration.md)
- [Architecture](./architecture.md)
