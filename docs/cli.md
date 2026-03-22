# CLI Reference

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-p` | | Prompt text, or `@file` to read from file |
| `-model` | `sonnet` | Model alias or `provider/model-id` |
| `-thinking` | `medium` | `off`, `minimal`, `low`, `medium`, `high` |
| `-max-turns` | 0 (unlimited) | Max agent turns per run |
| `-max-budget` | 0 (unlimited) | Max USD spend per run |
| `-yolo` | false | Disable sandbox and all permissions |
| `-permissions` | from config | `yolo`, `ask`, `auto` |
| `-permissions-model` | | Model for `auto` mode evaluator |
| `-path-scope` | derived | `workspace` or `unrestricted` |
| `-allow` | | Permission pattern (repeatable), e.g. `"Bash(go:*)"` |
| `-allow-path` | | Allow extra directory outside workspace (repeatable) |
| `-continue` | | Resume latest session |
| `-resume` | | Session browser, or `--resume <id>` for specific session |
| `-output` | `text` | `text` or `json` (JSON-lines) |
| `-login` | | `anthropic`, `openai`, `openai-transcribe` |
| `-logout` | | Remove stored credentials for provider |

## Serve subcommand

```bash
moa serve [--host 127.0.0.1] [--port 8080] [--model sonnet]
```

See [Web UI](./serve.md) for details.

## Model aliases

| Alias | Resolves to |
|-------|------------|
| `sonnet` | `claude-sonnet-4-6` |
| `opus` | `claude-opus-4-6` |
| `haiku` | `claude-haiku-4-5` |
| `codex` | `gpt-5.3-codex` |
| `codex-spark` | `gpt-5.3-codex-spark` |
| `codex-5.2` | `gpt-5.2-codex` |
| `gpt5` | `gpt-5.4` |
| `gpt5-mini` | `gpt-5.4-mini` |

You can also use canonical IDs (`claude-sonnet-4-6`) or provider-prefixed IDs (`anthropic/claude-sonnet-4-6`). Unknown IDs are accepted but context-window management is disabled for them.

## Examples

```bash
# one-shot prompt
moa -p "fix flaky tests"

# explicit provider/model
moa -model openai/gpt-5.3-codex -p "optimize this query"

# budget-limited run
moa -max-budget 0.50 -p "refactor auth module"

# permissions with allow patterns
moa -permissions ask -allow "Bash(go:*)" -allow "Write(*.go)"

# allow access to extra directory
moa -allow-path /tmp/shared-data

# web UI on the network
moa serve --host 0.0.0.0 --port 8080
```
