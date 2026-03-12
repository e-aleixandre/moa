# Configuration

Moa merges config from:

1. global: `~/.config/moa/config.json`
2. project: `<cwd>/.moa/config.json`

CLI flags override config at runtime.

In general, project config extends global config, while some preferences remain global-only (`pinned_models`, `trusted_mcp_paths`).

## Example

```json
{
  "disable_sandbox": false,
  "allowed_paths": ["/tmp"],
  "brave_api_key": "...",
  "pinned_models": ["claude-sonnet-4-6", "gpt-5.3-codex"],
  "trusted_mcp_paths": ["/Users/alice/work/project-a"],
  "mcp_servers": {
    "docs": {
      "command": "uvx",
      "args": ["my-mcp-server"],
      "env": {
        "API_KEY": "..."
      }
    }
  },
  "permissions": {
    "mode": "ask",
    "allow": ["Bash(git:*)", "read"],
    "deny": ["Bash(rm -rf:*)"],
    "model": "haiku",
    "rules": [
      "Never run package managers without approval",
      "Deny writes outside repository"
    ]
  }
}
```

## Fields

### `disable_sandbox` (bool)

If `true`, path sandboxing is disabled.

### `allowed_paths` ([]string)

Extra absolute paths allowed outside the workspace when sandboxing is enabled.

### `brave_api_key` (string)

Registers the `web_search` tool when present.

### `pinned_models` ([]string)

Models used by TUI `Ctrl+P` cycling.

> Global-only preference. Project-level `pinned_models` is ignored.

### `permissions`

- `mode`: `yolo | ask | auto`
- `allow`: glob policies auto-approved in `ask`
- `deny`: glob policies always denied
- `model`: evaluator model for `auto`
- `rules`: natural-language rules for the evaluator

### `mcp_servers`

Map of MCP server definitions loaded from config.

Each server supports:

- `command`
- `args`
- `env`

### `trusted_mcp_paths`

List of project directories whose `.mcp.json` files are trusted and may be auto-loaded.

> Global-only preference. Project-level `trusted_mcp_paths` is ignored.

## `.mcp.json`

Moa can also load MCP servers from `.mcp.json` using the Claude Code-compatible format:

```json
{
  "mcpServers": {
    "docs": {
      "command": "uvx",
      "args": ["my-mcp-server"]
    }
  }
}
```

Behavior:

- global `~/.config/moa/.mcp.json` is always loaded
- project `<cwd>/.mcp.json` is loaded only when the path is trusted
- in `moa serve`, trusted project MCP servers are loaded per session

## Policy pattern format

Permission patterns use `Tool(argPattern)` style, for example:

- `bash`
- `Bash(npm:*)`
- `Write(*.go)`
- `Edit(pkg/*)`

Matching is case-insensitive for tool names and supports glob-like argument matching.
