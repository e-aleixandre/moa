# Configuration

Moa merges config from:

1. global: `~/.config/moa/config.json`
2. project: `<cwd>/.moa/config.json`

CLI flags override config at runtime.

## Example

```json
{
  "disable_sandbox": false,
  "allowed_paths": ["/tmp"],
  "brave_api_key": "...",
  "pinned_models": ["claude-sonnet-4-6", "gpt-5.3-codex"],
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

If `true`, path sandboxing is disabled (YOLO filesystem access).

### `allowed_paths` ([]string)

Extra absolute paths allowed outside workspace when sandboxing is enabled.

### `brave_api_key` (string)

Brave Search key used to register `web_search` tool.

### `pinned_models` ([]string)

Models used by TUI `Ctrl+P` cycling.

> This is treated as a global preference; project-level `pinned_models` is ignored.

### `permissions`

- `mode`: `yolo | ask | auto`
- `allow`: glob policies auto-approved in `ask`
- `deny`: glob policies always denied
- `model`: evaluator model for `auto`
- `rules`: natural language rules for evaluator

## Policy pattern format

Patterns use `Tool(argPattern)` style:

- `bash`
- `Bash(npm:*)`
- `Write(*.go)`
- `Edit(pkg/*)`

Matching is case-insensitive for tool names and supports glob-like argument matching.
