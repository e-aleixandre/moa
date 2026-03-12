# Tools

## Registered by default

- `bash` — execute shell commands (streamed output, timeout, truncation + spill file)
- `read` — read text/image files with offset/limit support
- `write` — create/overwrite file
- `edit` — exact-text replacement with single-match enforcement
- `grep` — search file content (prefers `rg`)
- `find` — search files by glob (prefers `fd`)
- `ls` — list directory contents
- `fetch_content` — fetch URL and return readable markdown
- `subagent` — spawn child agent
- `subagent_status` — query async subagent jobs
- `subagent_cancel` — cancel async subagent jobs

## Conditionally registered

- `web_search` — enabled only when `brave_api_key` is configured

## Choosing the right tool

- use `grep`, `find`, and `ls` for exploration
- use `read` before editing files
- use `edit` for surgical changes
- use `write` for new files or full-file rewrites
- use `bash` when shell behavior is actually needed

## Sandbox behavior

Path-based tools use workspace sandboxing unless disabled by:

- `-yolo`
- `disable_sandbox: true`

Extra paths can be allowlisted via `allowed_paths`.

## Tool output truncation

Large outputs are truncated in-memory (head + tail strategy), and full outputs can be spilled to temp files for later inspection.

## Subagent usage

`subagent` parameters:

- `task` (required)
- `tools` (optional allowlist)
- `model` (optional)
- `thinking` (optional)
- `async` (optional bool)

Async flow:

1. call `subagent` with `async: true`
2. poll `subagent_status` with returned job id
3. optionally `subagent_cancel`
