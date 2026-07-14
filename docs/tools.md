# Tools

## Built-in tools

Always registered:

| Tool | Description |
|------|-------------|
| `bash` | Execute shell commands (streamed, timeout, truncation + spill file). Persists `cwd` and exported env between calls in a session |
| `bash_status` | Check a background bash job's status and output |
| `bash_wait` | Block until a background bash job finishes and return its result |
| `bash_cancel` | Cancel a running background bash job |
| `read` | Read text/image files with offset/limit |
| `write` | Create or overwrite files (atomic: temp file + rename) |
| `edit` | Exact-text replacement (single match enforced, atomic write) |
| `multiedit` | Atomic batch of edits to a single file |
| `apply_patch` | Apply multi-file unified diffs |
| `grep` | Search file content (prefers `rg` if installed) |
| `find` | Search files by glob (prefers `fd` if installed) |
| `ls` | List directory contents |
| `fetch_content` | Fetch a URL and extract readable markdown |
| `memory` | Read/update persistent cross-session project notes |
| `subagent` | Spawn a child agent (sync or async) |
| `subagent_status` | Poll async subagent jobs |
| `subagent_wait` | Block until an async subagent job finishes and return its result |
| `subagent_cancel` | Cancel a running async subagent |
| `tasks` | Track implementation tasks (used most heavily during plan mode, but always available) |

Conditionally registered:

| Tool | Condition |
|------|-----------|
| `web_search` | `brave_api_key` is configured |
| `ask_user` | TUI or web UI is active (not headless) |
| `verify` | `.moa/verify.json` exists |
| `load_skill` | At least one skill is discovered in `.moa/skills/` or `~/.config/moa/skills/` |

## Tool selection guidance

- Use `grep`, `find`, `ls` for exploration
- Use `read` before editing — `edit` warns if the file wasn't read first
- Use `edit` for surgical changes, `multiedit` for several changes in one file
- Use `apply_patch` for coordinated changes across multiple files
- Use `write` for new files or complete rewrites
- Use `bash` when you need actual shell behavior

## Bash: persistent state & background jobs

`bash` persists working directory and exported environment between calls within
a session: a `cd` or `export` in one call is visible in the next (an EXIT trap
captures `pwd` and `env -0` after each command). A few variables are never
persisted (`PWD`, `OLDPWD`, `SHLVL`, `_`, `BASH_ENV`, `ENV`, and exported bash
functions) because a real interactive shell regenerates them. Subagents get an
isolated copy seeded from their parent (subshell semantics: a child's `cd`/env
changes never propagate back).

Set `async: true` to launch long-running work in the background and get a job
ID: block on `bash_wait` when you need the result, peek with `bash_status`, or
stop it with `bash_cancel`. Background jobs do **not** persist `cwd`/env
changes. A synchronous call can't be promoted after launch — cancel and
relaunch with `async: true`.

## Sandbox

Path-based tools are sandboxed to the workspace directory by default. Escape attempts via `..` or symlinks are blocked.

Override with:
- `-yolo` flag
- `path_scope: "unrestricted"` in config
- `allowed_paths` for specific extra directories
- `/path add <dir>` at runtime in the TUI

### Dangerous-command confirmation

As a heuristic mitigation against prompt injection, `bash` commands that
download and immediately execute remote code (the `curl … | sh` shape, and its
`bash <(curl …)` / `sh -c "$(curl …)"` variants) always require explicit user
confirmation, even in permissive modes. This is not a sandbox — it only forces
a prompt — but it stops smuggled remote code from running unattended.

## Subagents

```
subagent(task: "...", model?: "...", thinking?: "...", tools?: [...], async?: bool)
```

Async flow: call with `async: true` → get a job ID → block on `subagent_wait` (preferred) or poll with `subagent_status` → optionally `subagent_cancel`.

### Live sub-conversations

A subagent is a full agent with its own streaming conversation, not just a
black box that returns text. While one runs, its activity (thinking, tool
calls, output) streams to the UI as it happens:

- **Web:** an *agent tray* appears above the input bar showing how many agents
  are working. Drag it up (or tap) to expand the list, then tap an agent to
  open its sub-conversation — rendered exactly like the main chat, updating
  live. A back arrow (or `Ctrl+G`) returns to the parent conversation. Async
  agents can be cancelled from the tray. The tray only lists *live* agents;
  finished ones drop off.
- **TUI:** press `Ctrl+G` to pick a live subagent and view its transcript in
  streaming; `Ctrl+G` or `Esc` returns.

The parent agent still receives the subagent's final text as the tool result,
so its own context is unchanged — the streaming view is purely for the user.

### Guardrails

Child agents run with their own, independent limits (they do **not** inherit
the parent's numbers, and have **no** budget/`$` cap of their own):

| Limit | Default | Config key (`config.json`) |
| --- | --- | --- |
| Max turns | 30 | `subagent_max_turns` |
| Max run duration | 10m | `subagent_max_run_duration` (Go duration, e.g. `"15m"`) |
| Max concurrent async jobs | 5 | `subagent_max_concurrent_async` |

Context compaction is disabled for children (they run short, focused tasks);
raising `subagent_max_turns` substantially may warrant enabling it.

Children cannot spawn their own subagents, use `memory`, or call `ask_user`.

### Cost & persistence

`subagent_status` reports a running/finished job's token usage and cost
(computed with the *child* model's pricing, which may differ from the parent).
The web UI shows each agent's cost separately from the session total.

Finished subagent transcripts are persisted to a side directory next to the
parent session (`<session-id>.subagents/<job-id>.json`), so they survive
restarts and can be reopened. They are removed when the parent session is
deleted.

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
