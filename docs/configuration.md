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
    "deny": ["Bash(rm -rf:*)"],
    "model": "haiku",
    "rules": ["Deny writes outside repository"]
  },
  "pinned_models": ["claude-sonnet-4-6", "gpt-5.3-codex"],
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

**Pattern format**: `Tool(argPattern)` — e.g. `Bash(npm:*)`, `Write(*.go)`, `Edit(pkg/*)`. Case-insensitive tool names, glob-like arguments.

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

## `AGENTS.md`

Moa discovers `AGENTS.md` files from the working directory upward and from `~/.config/moa/`. Their content is injected into the system prompt as project instructions. This is the main way to give the agent persistent context about your project.
