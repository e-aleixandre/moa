# Configuration

Moa loads config from two levels, merged together:

1. **Global**: `~/.config/moa/config.json`
2. **Project**: `<cwd>/.moa/config.json`

CLI flags override both at runtime. Project config extends global config; some fields are global-only (noted below).

## Example

```json
{
  "permissions": {
    "mode": "ask",
    "allow": ["Bash(git:*)", "read"],
    "deny": ["Bash(curl:*)", "Read(**/.env)"],
    "model": "haiku",
    "rules": ["Deny writes outside repository"]
  },
  "pinned_models": ["claude-sonnet-5", "gpt-5.3-codex"],
  "max_budget": 2.00,
  "max_turns": 100,
  "brave_api_key": "...",
  "mcp_servers": {
    "docs": {
      "command": "uvx",
      "args": ["my-mcp-server"],
      "env": { "API_KEY": "..." }
    }
  }
}
```

## Config fields

### Permissions

| Field | Type | Description |
|-------|------|-------------|
| `permissions.mode` | string | `yolo`, `ask`, `auto` |
| `permissions.allow` | []string | Glob patterns auto-approved in `ask` mode |
| `permissions.deny` | []string | Glob patterns always denied |
| `permissions.model` | string | Model for `auto` mode evaluator |
| `permissions.rules` | []string | Natural-language rules for the evaluator |

**Pattern format**: `Tool(argPattern)` — e.g. `Bash(npm:*)`, `Write(*.go)`, `Edit(pkg/*)`. Case-insensitive tool names, glob-like arguments. Arg scoping now applies to `grep`/`find`/`ls`/`multiedit` (matched on their `path`), `fetch_content` (on its `url`), and `apply_patch` (matched against every file the patch touches).

> **Bash deny is not a security boundary.** `Bash(...)` rules match the *literal command string* by prefix/glob. A rule like `Bash(rm -rf:*)` does **not** reliably block recursive deletes — it is trivially evaded by flag reordering (`rm -fr`, `rm -r -f`), absolute paths (`/bin/rm -rf`), a leading space, or shell aliases. Use `deny` to reduce accidents, not to contain an adversarial command. For real containment use `mode: ask`/`auto` (a human or model approves each call) and the path sandbox (`path_scope`).

### Paths & sandbox

| Field | Type | Description |
|-------|------|-------------|
| `path_scope` | string | `workspace` or `unrestricted` |
| `allowed_paths` | []string | Extra directories allowed outside workspace |
| `disable_sandbox` | bool | Deprecated — use `path_scope: "unrestricted"` |

### Limits

| Field | Type | Description |
|-------|------|-------------|
| `max_budget` | float | Max USD per run (0 = unlimited) |
| `max_turns` | int | Max agent turns per run (0 = unlimited) |
| `max_tool_calls_per_turn` | int | Max tool calls per turn (0 = unlimited) |
| `max_run_duration` | string | Go duration, e.g. `"30m"` (empty = unlimited) |

### Features

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `memory_enabled` | bool | `true` | Cross-session project memory |
| `auto_verify` | bool | `false` | Run verification checks automatically after changes |
| `brave_api_key` | string | | Enables the `web_search` tool |
| `cache_ttl` | string | `"5m"` | Interactive prompt-cache TTL. Only `"1h"` changes behavior; any other value falls back to the 5m default |
| `stt_language` | string | `"en"` | Speech-to-text language hint (ISO-639-1, e.g. `"es"`, `"en"`). Avoids Whisper mis-detecting short clips. Use `"auto"` to let the model detect |
| `persistent_shell` | bool | `true` | Whether `bash` persists working directory and exported env between calls in a session |

### Subagents

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `subagent_max_turns` | int | `100` | Max agent turns per subagent run (0 = package default) |
| `subagent_max_run_duration` | string | `"10m"` | Max subagent wall-clock duration, Go duration (empty = package default) |
| `subagent_max_concurrent_async` | int | `5` | Max concurrent async subagents (0 = package default) |

### Models

| Field | Type | Description |
|-------|------|-------------|
| `pinned_models` | []string | Models for `Ctrl+P` cycling. **Global-only.** |
| `plan_review_model` | string | Model for plan review (default: current model) |
| `plan_review_thinking` | string | Thinking level for plan review (default: `low`) |
| `code_review_model` | string | Model for code review |
| `code_review_thinking` | string | Thinking level for code review |

### MCP servers

| Field | Type | Description |
|-------|------|-------------|
| `mcp_servers` | map | MCP server definitions (see example above) |
| `trusted_mcp_paths` | []string | Project dirs whose `.mcp.json` is trusted. **Global-only.** |
| `trusted_project_paths` | []string | Project dirs whose `.moa/config.json` and `.moa/tools/*` are auto-loaded without a trust prompt. **Global-only.** |

Moa also loads `.mcp.json` files (Claude Code-compatible format):

- `~/.config/moa/.mcp.json` — always loaded
- `<cwd>/.mcp.json` — loaded only when the path is trusted

## Project directory: `.moa/`

Project-specific files live in `<cwd>/.moa/`:

| Path | Purpose |
|------|---------|
| `config.json` | Project config (merged with global) |
| `verify.json` | Verification commands for the `verify` tool |
| `tools/*.json` | Custom [script tools](./tools.md#custom-script-tools) |
| `prompts/` | Project prompt templates (override global `~/.config/moa/prompts/`) |

## Environment variables

| Variable | Purpose |
|----------|---------|
| `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` | Provider credentials (see [Quickstart](./quickstart.md)) |
| `MOA_CONFIG_DIR` | Overrides where the auth/credential store lives (default `~/.config/moa`). Useful for containers or custom deployments |
| `MOA_SERVE_TOKEN` | Shared secret for `moa serve` opt-in authentication; equivalent to `--token` (see [Web UI](./serve.md#security)) |

## `AGENTS.md`

Moa discovers `AGENTS.md` files from the working directory upward and from `~/.config/moa/`. Their content is injected into the system prompt as project instructions. This is the main way to give the agent persistent context about your project.
