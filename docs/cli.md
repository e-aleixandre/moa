# CLI Reference

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-p` | | Prompt text, or `@file` to read from file |
| `-model` | `sonnet` | Model alias or `provider/model-id` |
| `-thinking` | `medium` | `off`, `low`, `medium`, `high`, `xhigh` — see [Thinking levels](#thinking-levels) |
| `-max-turns` | 0 (unlimited) | Max agent turns per run |
| `-max-budget` | from config | Max USD spend per run (`-1` sentinel = use `config.json`; an explicit `0` means unlimited) |
| `-yolo` | false | Disable sandbox and all permissions |
| `-permissions` | from config | `yolo`, `ask`, `auto` |
| `-permissions-model` | | Model for `auto` mode evaluator |
| `-path-scope` | derived | `workspace` or `unrestricted` |
| `-allow` | | Permission pattern (repeatable), e.g. `"Bash(go:*)"` |
| `-allow-path` | | Allow extra directory outside workspace (repeatable) |
| `-output` | `text` | `text` or `json` (JSON-lines) |
| `-login` | | `anthropic`, `openai`, `xai` (SuperGrok/X OAuth device login), `meta` (Muse OAuth device login), `openai-transcribe` |
| `-logout` | | Remove stored credentials for provider |

### JSON-lines output

`-output json` emits one JSON object per line. In addition to lifecycle, message
updates, tool execution, progress, and error lines, it emits `message_usage`
for every completed assistant message (`role`, optional `subagent_id`,
`provider`, `model`, token fields, and `cost_usd`) and `subagent_end` with the
child's terminal `cost_usd`. Each `summary` includes total `cost_usd`, aggregate
`usage`, and `by_model` entries split by provider, model, and main/subagent role.

## Version subcommand

```bash
moa version   # or: moa --version, moa -v
```

Prints the version, commit, and build date.

## Update subcommand

```bash
moa update           # download, verify, and install the latest release
moa update --check   # only report current vs latest, install nothing
```

| Flag | Default | Description |
|------|---------|-------------|
| `--check` | false | Report whether an update is available without installing it |

Downloads the release archive for your platform, verifies its SHA-256 against
the release `checksums.txt`, and replaces the running binary in place. It never
restarts anything: restart Moa yourself afterwards.

Binaries installed through Homebrew or Nix are refused with a pointer to the
package manager (`brew upgrade moa`). If the binary's directory is not writable,
the command fails with a clear message rather than escalating privileges.
Unlike the passive update notice, `moa update` ignores `MOA_NO_UPDATE_CHECK`:
it is an explicit request.

## Serve subcommand

```bash
moa serve [--host 127.0.0.1] [--port 8080] [--model sonnet] [--token <secret>] [--allowed-hosts <names>]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--host` | `127.0.0.1` | Bind address (use `0.0.0.0` for remote access) |
| `--port` | `8080` | HTTP port |
| `--model` | `sonnet` | Default model for new sessions |
| `--token` | | Shared secret for opt-in auth (or `MOA_SERVE_TOKEN`). When set, requests need a valid session cookie or `?token=<secret>` |
| `--automation-token` | | Shared secret enabling the [Automation API](./automation.md) (or `MOA_AUTOMATION_TOKEN`), presented as `Authorization: Bearer <secret>`. Separate from `--token`; without it those routes do not exist |
| `--allowed-hosts` | | Comma-separated extra Host names accepted by the anti DNS-rebinding check (localhost/IP literals always allowed; e.g. a Tailscale MagicDNS name) |

See [Web UI](./serve.md) for details.

## Model aliases

| Alias | Resolves to |
|-------|------------|
| `sonnet` | `claude-sonnet-5` |
| `opus` | `claude-opus-5` |
| `haiku` | `claude-haiku-4-5-20251001` |
| `fable` | `claude-fable-5-1` |
| `codex` | `gpt-5.3-codex` |
| `codex-spark` | `gpt-5.3-codex-spark` |
| `codex-5.2` | `gpt-5.2-codex` |
| `grok` | `grok-4.6` (xAI) |
| `grok-4.6-build` | `grok-4.6` (the subscription backend's name for it) |
| `grok-4.5-build` | `grok-4.5` (the subscription backend's name for it) |
| `muse` | `muse-spark-1.3` (Meta) |
| `sol` | `gpt-5.6-sol` |
| `terra` | `gpt-5.6-terra` |
| `luna` | `gpt-5.6-luna` |
| `gpt-5.6` | `gpt-5.6-sol` |
| `gpt5` | `gpt-5.5` |
| `gpt5.5` | `gpt-5.5` |
| `gpt5-mini` | `gpt-5.4-mini` |

You can also use canonical IDs (`claude-sonnet-5`) or provider-prefixed IDs (`anthropic/claude-sonnet-5`). Some known models have no alias and are reachable only by ID: `claude-fable-5`, `claude-opus-4-8`, `grok-4.5`, `muse-spark-1.3-contributor` (cheaper, but Meta trains on its prompts). Provider-prefixed custom IDs, including `xai/<model-id>`, are accepted, but context-window management and any unverified pricing metadata are disabled for them.

## Thinking levels

`off`, `low`, `medium`, `high`, `xhigh` are the canonical levels, but what a
model does with them differs:

- **xAI Grok** requires reasoning: `off`/`low` collapse to `low`, `xhigh` to
  `high`. Only `low`, `medium`, `high` are distinct there.
- **Claude Fable 5.1** thinks on every turn. `off` is not a real setting for it
  and is promoted to `high`; the web selector hides the option.
- **`xhigh`** only reaches a higher tier on Anthropic Opus models. Every other
  Anthropic model caps it at `high`. OpenAI models accept `xhigh` as its own
  effort level.

## Fast mode

Fast mode buys premium speed at a premium price on the same model. It is a
per-session switch in the web UI (`GET`/`PATCH /api/sessions/{id}/fast`), not a
CLI flag, and only some models can serve it:

| Provider | Models that support it | What it costs |
|---|---|---|
| Anthropic | Opus models only | 2.5× faster, billed as separate usage credits |
| OpenAI | `gpt-5.4`, `gpt-5.5` and `gpt-5.6` generations (not the codex or mini variants) | 1.5× faster, burns credits 2.5× |
| xAI | the whole catalogue | priority queue, 2× the token rate |

Turning it on for a model that cannot serve it is not an error: the setting is
not stored, and the session stays at standard speed.

The session cost (`cost_usd`, the budget guardrail) charges a fast request at
the provider's premium: 2× on Anthropic ($10/$50 per MTok on Opus, cache
multipliers on top), 2.5× on OpenAI and 2× on xAI. The multiplier applies only
to turns the provider actually served at the premium tier — Anthropic reports
`usage.speed`, OpenAI and xAI echo `service_tier` — so a turn that fell back to
standard speed is billed as standard.

## Examples

```bash
# one-shot prompt
moa -p "fix flaky tests"

# explicit provider/model
moa -model openai/gpt-5.3-codex -p "optimize this query"

# Grok 4.6 with its supported thinking levels
moa -model grok -thinking high -p "review this change"

# budget-limited run
moa -max-budget 0.50 -p "refactor auth module"

# permissions with allow patterns
moa -permissions ask -allow "Bash(go:*)" -allow "Write(*.go)"

# allow access to extra directory
moa -allow-path /tmp/shared-data

# web UI on the network
moa serve --host 0.0.0.0 --port 8080
```
