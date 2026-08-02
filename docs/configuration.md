# Configuration

Moa loads config from two levels, merged together:

1. **Global**: `~/.config/moa/config.json`
2. **Project**: `<cwd>/.moa/config.json`

CLI flags override both at runtime. Project config extends global config; some fields are global-only (noted below).

Both files describe *what you configured*. What moa decides on its own while
you work — the approvals you granted with "always allow", the MCP servers you
switched off for a project — is kept separately, in
[your project state](#your-project-state), and never written into the project.

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
| `permissions.mode` | string | `yolo`, `ask`, `auto` (default `yolo`) |
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
| `stt_language` | string | `"en"` | Speech-to-text language hint (ISO-639-1, e.g. `"es"`, `"en"`). Avoids mis-detection on short clips. Use `"auto"` to let the model detect |
| `stt_model` | string | `"gpt-transcribe"` | Speech-to-text model. `"gpt-4o-mini-transcribe"` costs half as much per minute; `"whisper-1"` is the older, slower default |
| `stt_vocabulary` | string[] | `[]` | Words the transcriber keeps getting wrong (names, jargon, product names). Accumulates across scopes: a project adds its terms to your global ones. Keep it short — long lists make transcription worse (max 50 terms) |
| `persistent_shell` | bool | `true` | Whether `bash` persists working directory and exported env between calls in a session |
| `update_check` | bool | `true` | Check GitHub for a newer stable Moa release (six-hour ETag cache); set `false` to opt out |

Start `stt_vocabulary` empty and add words only once you catch the transcriber
getting them wrong. It is a hint, not a substitution: listing a word biases
spelling toward it, so a long list starts forcing your terms onto words that
merely sound similar. Because it accumulates, your own name belongs in the
global config and a project's jargon in its `.moa/config.json`:

```json
{ "stt_vocabulary": ["goreleaser", "Preact", "esbuild"] }
```

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
| `disabled_mcp_servers` | []string | Server names vetoed at this config level: the server stays configured but is never started. In a project file this is legacy — moa now records your vetoes in [your project state](#your-project-state) |
| `trusted_mcp_paths` | []string | Project dirs whose `.mcp.json` is trusted. **Global-only.** |
| `trusted_project_paths` | []string | Project dirs whose `.moa/config.json` and `.moa/tools/*` are auto-loaded without a trust prompt. **Global-only.** |

Moa also loads `.mcp.json` files (Claude Code-compatible format):

- `~/.config/moa/.mcp.json` — always loaded
- `<cwd>/.mcp.json` — loaded only when the path is trusted

#### Transports: local command or remote URL

A server entry declares **exactly one** transport:

- `command` (+ optional `args`, `env`) — stdio: Moa spawns the server as a local
  subprocess.
- `url` (+ optional `headers`) — streamable HTTP: Moa connects to a remote
  endpoint. Only `http` and `https` are accepted.

```json
{
  "mcpServers": {
    "local": { "command": "uvx", "args": ["my-mcp-server"] },
    "relay": {
      "url": "https://relay.example.com/mcp",
      "headers": { "Authorization": "Bearer ..." }
    }
  }
}
```

Setting both `command` and `url` — or neither — is a configuration error and the
file is rejected. `headers` are the **only** supported authentication mechanism
for a remote server: they are sent on every request to that endpoint, so it is
where credentials go; they are stored in plain text in the config file like any
other key there. Credentials embedded in the URL itself
(`https://user:pass@host/mcp`) are rejected — the URL is shown in the MCP panel
and written to logs, so it is not a place for secrets.

Redirects are never followed: `headers` would be re-sent to whatever origin a
`30x` points at, so a redirecting endpoint fails the request instead.

A remote server has no process to supervise: it shows up in the MCP panel like
any other server, and enable/disable/restart just drop and re-dial the
connection. If the connection is lost the server is reported as exited and can be
restarted. The endpoint is an outbound connection to an address **you**
configured — Moa applies no network policy beyond the scheme check, exactly as
with automation `callback_url`s.

## Project directory: `.moa/`

Project-specific files live in `<cwd>/.moa/`:

| Path | Purpose |
|------|---------|
| `config.json` | Project config (merged with global) |
| `verify.json` | Verification commands for the `verify` tool |
| `tools/*.json` | Custom [script tools](./tools.md#custom-script-tools) |
| `prompts/` | Project prompt templates (override global `~/.config/moa/prompts/`) |

## Your project state

`~/.config/moa/projects/<hash>/state.json` holds what moa records about a
project **on your behalf**, kept out of the repository:

| Field | Written when |
|-------|--------------|
| `permission_allow` | You approve a tool call with "always allow" |
| `disabled_mcp_servers` | You switch a server off with the `project` scope |
| `config` | Never — this one is yours to edit (see below) |

The split follows who the decision belongs to. `.moa/config.json` describes the
project — which MCP servers it uses, what its limits are — and is meant to be
committed and shared. Approving a command on your machine is not that: it used
to be appended to the project's config file, so a click produced a diff nobody
wanted to commit, and it followed the repository to everyone who cloned it.

This also keeps moa from writing into the checkout at all during a normal
session, which is what previously made a shared checkout unusable: the file was
created with private permissions, so whoever approved something first owned it.

Consequences worth knowing:

- A `project`-scoped MCP veto is now yours alone; it no longer travels with the
  repository. To keep a server out of a project for everybody, don't declare it
  in `.moa/config.json`.
- The `project` scope no longer requires trusting the project, because nothing
  is written there. Trust still governs whether the project's own config,
  script tools and `.mcp.json` are loaded.
- `allow` patterns already present in a `.moa/config.json` keep working, and so
  does a `disabled_mcp_servers` entry left there by an older moa. Moa cannot
  tell a deliberate team policy from a leftover click, so nothing is migrated
  or removed — move them yourself if you want them gone.
- The state is per workspace path, hashed the same way [memory](./tools.md#memory)
  scopes its project facts. Moving a checkout starts a fresh state.

### Settings for one project, without touching the repository

The `config` block takes the same fields as `config.json`, for settings you
want in this project but not in its repository — a turn limit you prefer here,
your own review model:

```json
{
  "config": {
    "max_turns": 40,
    "code_review_model": "haiku"
  }
}
```

Moa never writes this block; it is yours to edit. It is applied after global
config and after the project's own, so it wins over both — but through the same
merge rules, so `max_turns`, `max_tool_calls_per_turn` and `max_run_duration`
can only be **tightened**: if your global config caps runs at 50 turns, asking
for 500 here still gives you 50. `max_budget` is the exception and takes any
explicit value, matching how the project's own config has always behaved. No
trust prompt is involved, because the file is yours rather than the checkout's.

It takes the same fields as `config.json`, which includes the ones that widen
what moa may do — `permissions`, `path_scope`, `allowed_paths`, `mcp_servers`.
That is intended, since the file is your own and no repository can write to it,
but it does mean a stray `"path_scope": "unrestricted"` here applies with no
prompt. It cannot grant trust: `trusted_project_paths` and `trusted_mcp_paths`
are global-only and ignored at this level, so a project cannot come to trust
itself through it.

Use it when the setting is about *how you work here*, and `.moa/config.json`
when it is about the project itself and should reach everyone who clones it.

## Environment variables

| Variable | Purpose |
|----------|---------|
| `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` | Provider credentials (see [Quickstart](./quickstart.md)) |
| `MOA_CONFIG_DIR` | Moves everything moa stores about itself (default `~/.config/moa`): `config.json`, credentials, sessions, skills, prompts, memory, attachments. See [Running two instances](#running-two-instances) |
| `MOA_SERVE_TOKEN` | Shared secret for `moa serve` opt-in authentication; equivalent to `--token` (see [Web UI](./serve.md#security)) |
| `MOA_AUTOMATION_TOKEN` | Shared secret enabling the inbound [Automation API](./automation.md); equivalent to `--automation-token`. Separate from `MOA_SERVE_TOKEN` |
| `MOA_NO_UPDATE_CHECK=1` | Disables the best-effort GitHub release check for this process |
| `MOA_ATTACHMENTS_DIR` | Base directory for `moa serve` attachment staging (default `/tmp/moa-<uid>`); detailed in [Web UI](./serve.md) |

## Running two instances

Everything moa keeps about itself lives in one directory, `~/.config/moa` by
default. `MOA_CONFIG_DIR` moves all of it — config, credentials, sessions,
skills, prompts, memory, attachments — so two instances can run on one machine
without sharing anything:

```bash
MOA_CONFIG_DIR=~/.config/moa-work moa serve --port 8081
```

Each instance keeps its own API keys, model defaults, MCP servers and history.
The one thing outside it is the temporary staging area for web-UI attachments,
shared per user account and overridable with `MOA_ATTACHMENTS_DIR`.

The variable points moa at a *different* directory; it does not move what is
already in the old one. A fresh `MOA_CONFIG_DIR` therefore starts empty — no
credentials, no history, no skills — and the previous state stays untouched in
`~/.config/moa`. Copy across whatever you want to keep:

```bash
cp ~/.config/moa/auth.json ~/.config/moa-work/
```

If you were already setting `MOA_CONFIG_DIR` before moa 0.24, note that it used
to move only part of the state: credentials and attachments followed it, while
`config.json`, sessions, skills and prompts stayed in the home directory. Now
that all of it moves together, that leftover state is no longer read — move the
files you still want into the override directory.

### Two people on one machine

Give each person a Unix account instead. Separate accounts already separate
everything above, and the isolation is enforced by the operating system rather
than by moa: one account cannot read the other's credentials even if moa has a
bug.

Give each account its own checkout as well. A single checkout shared through
group permissions does not currently work: moa writes `<project>/.moa/` with
private permissions, so whoever triggers the first write owns it and the other
account can no longer read the project's `verify.json`, skills or prompts.

Moa is a single-user tool by design. It has no notion of who is asking: no
per-user permissions, no ownership on sessions, no way to scope what one
person's agent may touch. Sharing one moa between two people means sharing one
identity and one set of credentials, so keep the accounts separate rather than
expecting moa to tell them apart.

## `AGENTS.md`

Moa discovers `AGENTS.md` files from the working directory upward and from `~/.config/moa/`. Their content is injected into the system prompt as project instructions. This is the main way to give the agent persistent context about your project.
