# Tools

## Built-in tools

Always registered:

| Tool | Description |
|------|-------------|
| `bash` | Execute shell commands (streamed, timeout, truncation + spill file). Persists `cwd` and exported env between calls in a session |
| `bash_status` | Check a background bash job's status and output |
| `bash_wait` | Block until a background bash job finishes and return its result |
| `bash_cancel` | Cancel a running background bash job |
| `read` | Read text, image and PDF files with offset/limit (PDF needs `pdftotext`) |
| `write` | Create or overwrite files (atomic: temp file + rename) |
| `edit` | Text replacement that tolerates whitespace/indentation differences; the match must be unique unless `replaceAll` is set (atomic write) |
| `multiedit` | Atomic batch of edits to a single file |
| `apply_patch` | Add, delete and update several files in one `*** Begin Patch` block |
| `grep` | Search file content (prefers `rg` if installed) |
| `find` | Search files by glob (prefers `fd` if installed) |
| `ls` | List directory contents |
| `fetch_content` | Fetch a URL and extract readable markdown |
| `memory` | Search/read/update persistent cross-session notes |
| `subagent` | Spawn a child agent (sync or async) |
| `subagent_status` | Poll async subagent jobs |
| `subagent_wait` | Block until an async subagent job finishes and return its result |
| `subagent_cancel` | Cancel a running async subagent |
| `subagent_steer` | Send instructions or a correction to a subagent that is still running |
| `tasks` | Your checklist, requests to the owner (`ask`, which does not block you) and your project's backlog (`claim`). Shared through the [task database](./serve.md#tasks); an agent only sees its own checklist and requests plus the project backlog. `create` with `when` (plain text: `in 20m`, `tomorrow at 09:00`, `every monday at 09:00`, in English or Spanish) [schedules](./serve.md#scheduled-tasks) the task for this session only: it is read in the timezone of the device that created the session (UTC, said in the result, when unknown), an unqualified hour such as `at 9` is refused with both readings so nothing is scheduled by accident, and `list` shows these apart from the checklist. `comment` writes in the thread of a task between owners or of your own request; a task another owner sent you is closed with `done`, `outcome` (`done` or `cant_do`) and a `note`. In an owner's conversation, `send` gives a task to another owner ([Tasks between owners](./serve.md#tasks-between-owners)) and `list` shows it under Sent |
| `verify` | Run the project's [verification checks](#verify) |
| `load_skill` | Load a [skill](./serve.md#skills-and-reload) by name. Skills are discovered per call, so one written mid-session is loadable. Most skills return their content; `context: fork` runs an isolated subagent instead |
| `moa_docs` | Read moa's own documentation (this page included), embedded in the binary |

Conditionally registered:

| Tool | Condition |
|------|-----------|
| `web_search` | `brave_api_key` is configured |
| `ask_user` | The web UI is active (not headless) |
| `run_with_secret` | The web UI is active. Runs a command in a terminal and types the password the owner enters on a card at its password prompts. See [Passwords for one action](#passwords-for-one-action) |
| `browser_fill_secret` | The web UI is active and an MCP server exposes `browser_type` and `browser_evaluate` (Playwright). Types what the owner enters on a card into fields of the open page. See [Passwords for one action](#passwords-for-one-action) for its limits |

In a `moa serve` session the agent also gets `send_file`, which publishes a file
to the conversation's [Artifacts](./serve.md#files-sent-by-the-agent) as well
as showing its download card in the chat. Custom
[script tools](#custom-script-tools) and [MCP](./configuration.md#mcp-servers)
tools are registered on top of all of this.

In a codebase that has a [project owner](./owners.md), every session also gets
`book`, read-only, to read the owner's book. The owner's own conversation gets
`book` with write access, `sessions` to list, read, message, start and answer
the sessions of its codebase, and the built-in skills its role relies on
through `load_skill`.

## Tool selection guidance

- Use `grep`, `find`, `ls` for exploration
- Use `read` before editing — `edit` and `multiedit` refuse a file that wasn't read first
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

`memory` stores single-fact notes that survive across sessions. Each fact
declares its scope: `global` facts are visible in every project, `project`
facts are scoped to the current **repository** —
every git worktree of one repo reads and writes the same project facts, and
removing a worktree no longer strands what was learned in it. Files written
before 0.26 carry the older four-value `type` (`user`/`feedback` → global,
`project`/`reference` → project); they are still read, and a rewrite records
`scope` while keeping a compatible `type`.

Only an **index** — one line per fact — is injected into the prompt; full bodies
are read on demand. The index has a byte budget, and each scope gets a reserved
share of it with the leftover rolling over in both directions. Without that
reservation the more numerous project facts crowd out the global ones entirely,
which is the wrong trade: a small cross-project fact should not disappear only
because one repository has many project facts.

Memory is a last resort for a small, non-secret fact that is costly to
reconstruct and has no discoverable canonical source. A short pointer to an
external canonical source can be appropriate; copying that source into memory
is not. Memory is not a task tracker, scratchpad, instruction file, workflow
library, or secret store. Every write must declare either
`invalidate_when`, a single-line natural-language condition under which the
fact stops being true, or `durable: true` for a genuinely permanent fact. The
condition must be independently checkable now against a concrete source, not a
judgment about relevance: for example, "when issue #84 is closed", "when `git
log` shows branch X is merged", or "when port 3306 on that host responds
again". "When it is no longer relevant" is not a valid condition. Use
`durable: true` only for an otherwise eligible fact with no identifiable
invalidating event. Lifecycle is required, but it does not make rules,
preferences, procedures, task state, secrets, or copied project knowledge
eligible. If an identifiable event could make a fact false, use
`invalidate_when` instead of `durable`.
When reading a fact with an invalidation condition, delete it if you can verify
that the condition has occurred.

The write parameters are `name`, `description`, `scope`, `content`, and exactly
one of `invalidate_when` or `durable`. The description is a one-line hook of at
most 180 bytes — it is paid for on every turn, so the detail belongs in
`content`. `invalidate_when` is stored with the
fact and is shown by `memory` `read`; it is intentionally omitted from the
always-in-context `list` index. A successful write reports how much of the
index budget is in use, and how many facts overflow it and therefore never
reach the prompt.

`memory` `search` finds facts by text across names, descriptions and bodies in
both scopes, returning ids and a short snippet around each match rather than
whole bodies. It takes `query`, an optional `regex` flag (Go/RE2), `limit`
(default 10, max 25) and `offset`.

Other memory rules:

- **Anything with a discoverable canonical source does not belong here.** Read
  or reference AGENTS.md, skills, code, tests, configuration, documentation,
  scripts, tool definitions, issues, or other versioned artifacts instead of
  restating them.
- Standing rules, preferences, conventions and prohibitions belong in
  `AGENTS.md`. Reusable workflows belong in skills; executable procedures
  belong in scripts or registered tools.
- Credentials, tokens, keys, private signed URLs and other secrets belong in
  protected configuration, environment variables or a secret manager, never
  in memory.
- Prefer updating an existing fact over adding a near-duplicate, and delete
  facts that have become wrong, expired, or gained a canonical source.
- Current task state, progress notes and handoffs are not memories. Use the task
  tracker, or the session checkpoint only for active, non-reconstructible
  pre-compaction state.

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
- `/path add <dir>` at runtime in the web UI

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
The parent agent still receives the subagent's final text as the tool result,
so its own context is unchanged — the streaming view is purely for the user.
When a child fails and its transcript can be reopened, its failure message
preserves the underlying error and any partial output, and tells the parent it
can continue the saved job with `resume: "<job-id>"` rather than starting over.

A resumed subagent keeps the model and thinking level it already ran under, so
continuing a `sol` or `fable` child does not silently switch it to the parent's
current model. Passing `model` or `thinking` explicitly always wins, both on a
fresh subagent and on a resume. Transcripts saved before those fields were
recorded — or pointing at a model that no longer exists — fall back to the
parent's model/thinking instead of failing the resume.

### Guardrails

Child agents run with their own, independent limits (they do **not** inherit
the parent's numbers, and have **no** budget/`$` cap of their own):

| Limit | Default | Config key (`config.json`) |
| --- | --- | --- |
| Max turns | 100 | `subagent_max_turns` |
| Max run duration | 10m | `subagent_max_run_duration` (Go duration, e.g. `"15m"`) |
| Max concurrent async jobs | 5 | `subagent_max_concurrent_async` |

Context compaction is enabled for children, with the same threshold as the main
session, so a long-running child won't fail by exhausting its turn budget.
Tool-result trimming is not: a child goes straight to compaction. What
a child never gets is the pre-compaction step (`compact_strategy`): it has
neither `memory` nor the ephemeral checkpoint to write to, so a warning could
only produce stray files, and its findings already travel back in its report.

Children cannot spawn their own subagents, use `memory`, call `ask_user`, use
`checkpoint`, or direct other sessions with `sessions`; a child of the project
owner reads the `book` but cannot write it. A child started by a forked skill
has that same denylist.

### Cost & persistence

`subagent_status` reports a running/finished job's token usage and cost
(computed with the *child* model's pricing, which may differ from the parent).
In `moa serve` it also answers for a job that has already left memory, reading
its outcome from the saved transcript; a transcript left unfinished by a
restart is reported as interrupted and can be continued with `resume`.
The web UI shows each agent's cost separately from the session total.

Finished subagent transcripts are persisted to a side directory next to the
parent session (`<session-id>.subagents/<job-id>.json`), so they survive
restarts and can be reopened. They are removed when the parent session is
deleted.

For Anthropic subagents, `moa serve` also keeps a prompt-cache audit per job in
`<session-id>.subagents/<job-id>.cache.json`, removed together with the
session. It holds only SHA-256 digests of the final request (whole body,
tools, system, options, a rolling hash per block) and the position and TTL of
each cache marker — never prompt text, signatures, images or credentials. It
records the first and the latest request plus `count`: the number of final
request bodies built for that job. The file is written when the first request
is built and again when the job ends, so if moa crashes mid-job only the first
request (`count` 1, no `last`) is kept; `last` is also absent if it could not be
computed. Each resume is a new job with its own file,
so counts are per job, not summed across resumes. An HTTP retry resends the
same body and counts once; a fast-mode fallback rebuilds the body and counts
again. A resumed job's `resumed_from` names the job it continued: compare the
original job's `last` with the resumed job's `first` to see where the prefix
diverged. Treat this file as private: hashes can confirm guessed content; do not share it publicly.
The terminal UI does not persist subagents and keeps no audit.

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

## Passwords for one action

`run_with_secret` and `browser_fill_secret` let the agent use a password the
owner types on a card, for one action the card shows. Every request shows a
card, whatever the permission mode. Only someone using the web UI or the paired
app can answer it: an owner session cannot. A card waits until it is answered,
and Stop cancels it.

### Commands (`run_with_secret`)

- The card shows the exact command, its working directory and, for `ssh`, the
  host. That command runs in a terminal (a PTY), so programs that read the
  password from the terminal (`sudo`, `ssh`, `security unlock-keychain`) work.
  For a remote command use `ssh -tt host …` so the remote program gets a
  terminal too.
- The value is typed at every password prompt of that one run, with no limit.
  The run stops without resending it when a prompt comes back after a
  recognisable authentication failure (`Sorry, try again`, `incorrect`,
  `denied`…). It is never typed after Stop or after the command has ended.
  Nothing is kept for the next command.
- The command runs with moa's own environment, not with variables exported by
  earlier `bash` calls.
- Values with line breaks or other control characters are refused. A terminal
  line holds at most 4,095 bytes, so a longer value reaches the program cut
  short.
- The value does not go to the chat, the transcript, the model or any event.
  Where the command's output contains it (for example an echo), it shows as
  `[secret]`.
- **Limit:** the command receives the password and can do anything with it,
  such as save it or send it elsewhere. Redaction only catches the exact value
  in the output, not a transformed copy. Read the command on the card before
  allowing it.

### Web pages (`browser_fill_secret`)

The page's origin and the fields' real labels shown on the card come from the
browser. The fill response is redacted. This tool does **not** keep the password
out of every place, and these limits are known and accepted:

- **Which server receives it.** The tool uses the first MCP server that offers
  `browser_type` and `browser_evaluate`. Any such server can therefore receive
  the password and report any origin, including one added for a single
  Automation API run. Its answers are what the card shows.
- **Frames.** The origin is checked again just before typing, but against the
  top-level page, not the frame the field is in, and only once before the
  first field. A field inside an iframe that navigated to another site after
  the card appeared would still be filled, and so would a page that navigates
  between that check and the typing, or between two fields.
- **Files on disk.** Playwright can write a snapshot file for the fill itself
  (for example under its `--output-dir`), and that file contains the field's
  value. moa does not delete or redact it. Images returned by the server are
  stored before the redaction runs, and audio or other content converted to
  text can reach the chat and the model in an encoded form.
- **Escaping.** A password that mixes characters Playwright escapes in its own
  way (for example a backspace together with an apostrophe) can appear escaped
  in the fill response, where the redaction does not recognise it.
- **Later snapshots.** Any later look at the page with the field still filled
  (`browser_snapshot`, `browser_evaluate`, a screenshot) can show the value in
  the chat and send it to the model.

### Saving on the iPhone

In the iOS app the card offers **Save on this iPhone** for fields the agent
gave an `alias`. The value is kept in this
iPhone's Keychain behind Face ID, only for the paired server, and is not synced.
The next time the same exact action is requested, **Allow with Face ID** uses it
in one tap. If the action is different, the card says so and the saved value has
to be filled with Face ID before allowing. For a command, the same action means
the same command, directory and host. For a page, it means the same origin,
the same fields in the same order (label, type and kind), and the same submit. Settings › Saved passwords lists
the saved values and forgets them. Unpairing forgets them all; if the Keychain cannot delete them, the app says
so and asks to unpair again. Each use sends
the value to the server, just like typing it.

