# Tools

## Built-in tools

Always registered:

| Tool | Description |
|------|-------------|
| `bash` | Execute shell commands (streamed, timeout, truncation + spill file) |
| `read` | Read text/image files with offset/limit |
| `write` | Create or overwrite files |
| `edit` | Exact-text replacement (single match enforced) |
| `multiedit` | Atomic batch of edits to a single file |
| `apply_patch` | Apply multi-file unified diffs |
| `grep` | Search file content (prefers `rg` if installed) |
| `find` | Search files by glob (prefers `fd` if installed) |
| `ls` | List directory contents |
| `fetch_content` | Fetch a URL and extract readable markdown |
| `memory` | Read/update persistent cross-session project notes |
| `subagent` | Spawn a child agent (sync or async) |
| `subagent_status` | Poll async subagent jobs |
| `subagent_cancel` | Cancel a running async subagent |

Conditionally registered:

| Tool | Condition |
|------|-----------|
| `web_search` | `brave_api_key` is configured |
| `ask_user` | TUI or web UI is active (not headless) |
| `tasks` | Plan mode is active |
| `verify` | `.moa/verify.json` exists |

## Tool selection guidance

- Use `grep`, `find`, `ls` for exploration
- Use `read` before editing — `edit` warns if the file wasn't read first
- Use `edit` for surgical changes, `multiedit` for several changes in one file
- Use `apply_patch` for coordinated changes across multiple files
- Use `write` for new files or complete rewrites
- Use `bash` when you need actual shell behavior

## Sandbox

Path-based tools are sandboxed to the workspace directory by default. Escape attempts via `..` or symlinks are blocked.

Override with:
- `-yolo` flag
- `path_scope: "unrestricted"` in config
- `allowed_paths` for specific extra directories
- `/path add <dir>` at runtime in the TUI

## Subagents

```
subagent(task: "...", model?: "...", thinking?: "...", tools?: [...], async?: bool)
```

Async flow: call with `async: true` → get a job ID → poll with `subagent_status` → optionally `subagent_cancel`.

## Custom script tools

Define tools as JSON files in `.moa/tools/`:

```json
// .moa/tools/deploy.json
{
  "name": "deploy",
  "description": "Deploy to staging",
  "command": "bash scripts/deploy.sh staging"
}
```

Each file defines one tool that runs a shell command. The tool is registered automatically when Moa starts in that project.

## Verify

Define project checks in `.moa/verify.json`:

```json
{
  "checks": [
    { "name": "build", "command": "make build" },
    { "name": "test", "command": "make test" },
    { "name": "lint", "command": "make lint" }
  ]
}
```

Run with `/verify` in the TUI, or automatically after changes if `auto_verify` is enabled in config.
