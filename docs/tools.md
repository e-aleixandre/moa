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
| `moa_docs` | Read moa's own documentation (this page included), embedded in the binary |

Conditionally registered:

| Tool | Condition |
|------|-----------|
| `web_search` | `brave_api_key` is configured |
| `ask_user` | TUI or web UI is active (not headless) |
| `verify` | always |
| `load_skill` | At least one skill is discovered in `.moa/skills/` or `~/.config/moa/skills/` |

## Tool selection guidance

- Use `grep`, `find`, `ls` for exploration
- Use `read` before editing — `edit` warns if the file wasn't read first
- Use `edit` for surgical changes, `multiedit` for several changes in one file
- Use `apply_patch` for coordinated changes across multiple files
- Use `write` for new files or complete rewrites
- Use `bash` when you need actual shell behavior

## Self-documentation

`moa_docs` returns the pages of this documentation set from inside the binary.

Installing moa gets you a single static binary, with no copy of the repository
and nothing next to it on disk. Without this the agent could only answer
questions about moa from whatever it happened to remember — which is how
confident, wrong answers about flags and config files get produced. So a user
who is working on their own project can ask moa to write them a
`.moa/verify.json`, or explain how to trigger a run from a webhook, and get an
answer from the real documentation without cloning anything or opening a
browser.

Because the pages are embedded at build time, they always describe the exact
version being run: updating the binary updates its documentation with it.

The page names are listed in the tool description, which is the only cost this
carries in the system prompt — page content is read on demand, never
preloaded.

```
moa_docs(page: "configuration")   # config.json fields and precedence
moa_docs(page: "automation")      # HTTP API for triggering runs
moa_docs(page: "recipes/linear")  # worked end-to-end integration
```

## Memory

`memory` stores single-fact notes that survive across sessions. Each fact has a
type that decides its scope: `user` and `feedback` are global (visible in every
project), `project` and `reference` are scoped to the current **repository** —
every git worktree of one repo reads and writes the same project facts, and
removing a worktree no longer strands what was learned in it.

Only an **index** — one line per fact — is injected into the prompt; full bodies
are read on demand. The index has a byte budget, and each scope gets a reserved
share of it with the leftover rolling over in both directions. Without that
reservation the more numerous project facts crowd out the global ones entirely,
which is the wrong trade: global facts are standing instructions and user
preferences, the ones that do not expire.

Memory is for non-obvious facts with an explicit lifecycle. It is not a task
tracker and not a scratchpad. Every write must declare either
`invalidate_when`, a single-line natural-language condition under which the
fact stops being true, or `durable: true` for a genuinely permanent fact. The
condition must be independently checkable now against a concrete source, not a
judgment about relevance: for example, "when issue #84 is closed", "when `git
log` shows branch X is merged", or "when port 3306 on that host responds
again". "When it is no longer relevant" is not a valid condition. Use
`durable: true` for user preferences, repository conventions, and procedures.
If an identifiable event could make a fact false, use `invalidate_when`
instead of `durable`.
When reading a fact with an invalidation condition, delete it if you can verify
that the condition has occurred.

The write parameters are `name`, `description`, `type`, `content`, and exactly
one of `invalidate_when` or `durable`. `invalidate_when` is stored with the
fact and is shown by `memory` `read`; it is intentionally omitted from the
always-in-context `list` index.

Other memory rules:

- **Anything verifiable by looking at the repository does not belong here.** A
  fact should point at the code rather than restate it, or it will quietly go
  stale as the code changes.
- Prefer updating an existing fact over adding a near-duplicate, and delete
  facts that have become wrong.
- Current task state, progress notes and handoffs are not memories — they die
  with the work. Use the session checkpoint or the task tracker.

Project facts written before 0.25 lived under `~/.config/moa/projects/<hash>/`,
keyed by the path of the working directory. moa copies them into the
repository-keyed store the first time it opens each project, and leaves the old
files alone so an older binary still finds them. A store whose directory no
longer exists cannot be matched to a repository — git cannot say which repo a
deleted worktree belonged to — so it is listed once in
`~/.config/moa/codebases/orphaned-memory.json` with its path and fact count
instead of being guessed at. Starting a session in that directory again, if it
comes back, migrates it.

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
  finished ones drop off. At completion the parent timeline receives exactly one
  terminal outcome card keyed to the job: a completed child has **Result** (an
  explicitly bounded excerpt is labelled as such), a failed child has **Error**,
  and a cancelled child has no result. **Conversation** always opens the full
  child transcript. This card is independent of whether the parent model got
  the text through the normal async notification or through `subagent_wait`.
- **TUI:** press `Ctrl+G` to pick a subagent and view its transcript in
  streaming; `Ctrl+G` or `Esc` returns. Its terminal block has the same
  completed/failed/cancelled outcome semantics. Unlike serve, the TUI
  keeps child transcripts only for its current process, so it cannot reopen a
  completed child after switching sessions or restarting.

The parent agent still receives the subagent's final text as the tool result,
so its own context is unchanged — the streaming view is purely for the user.

### Guardrails

Child agents run with their own, independent limits (they do **not** inherit
the parent's numbers, and have **no** budget/`$` cap of their own):

| Limit | Default | Config key (`config.json`) |
| --- | --- | --- |
| Max turns | 100 | `subagent_max_turns` |
| Max run duration | 10m | `subagent_max_run_duration` (Go duration, e.g. `"15m"`) |
| Max concurrent async jobs | 5 | `subagent_max_concurrent_async` |

Context compaction is enabled for children, with the same defaults as the main
session, so a long-running child won't fail by exhausting its turn budget.

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

Each file defines one tool that runs a shell command. The tool is registered
automatically when Moa starts in that project — but only for directories the
user has explicitly *trusted* (like `.mcp.json` and repo-local config), so an
untrusted repo can't register shell-executing tools that auto-run at the first
prompt.

Optional fields and parameters:

- `timeout` — max seconds the command may run before it's killed (default `60`).
- `args` — a runtime tool parameter (string). When supplied, it's passed
  positionally to the command, which can reference it as `"$1"`, `"$@"`, etc.
  Passing it positionally (not interpolated into the command) avoids shell
  injection.

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

Run with `/verify`, or automatically after changes if `auto_verify` is enabled in config.

### Verifying another repository or worktree

Checks run in the session's working directory by default. When a session's work
spans several checkouts — the conversation starts in one repository and the code
being changed lives in another worktree — point verify at the other directory
instead of editing `.moa/verify.json` to reach across:

```
/verify ../other-worktree
```

The agent can do the same through the tool's `cwd` parameter. Relative paths
resolve against the session directory, and the target's own `.moa/verify.json`
is the one that runs.

The directory must be one the session is allowed to touch: running a
`.moa/verify.json` means running the shell commands inside it, so the sandbox
applies here as it does everywhere else. If it is refused, allow it with
`/path add <dir>`. Sessions running unrestricted (YOLO) can target any
directory.

The `verify` tool is available even when the session's own directory has no
`.moa/verify.json` — otherwise it would be missing from exactly the multi-repo
sessions that need it. Called with nothing to run, it says so.
