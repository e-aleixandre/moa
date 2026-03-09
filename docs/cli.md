# CLI Reference

## Flags

```text
-p                  Prompt text or @file
-model              Model alias or provider/model-id (default: sonnet)
-thinking           off|minimal|low|medium|high (default: medium)
-max-turns          Max agent turns (default: 50)
-yolo               Disable sandbox and permissions
-permissions        yolo|ask|auto
-permissions-model  Model used by auto permission evaluator
-continue           Resume latest saved session
-resume             Open session browser (or --resume <id>)
-login              Login to provider: anthropic | openai
-logout             Remove stored credentials for provider
```

## Model selection

Accepted formats:

- alias: `sonnet`, `opus`, `haiku`, `codex`, `codex-spark`, `codex-5.2`
- canonical id: `claude-sonnet-4-6`
- provider/id: `anthropic/claude-sonnet-4-6`, `openai/gpt-5.3-codex`

Unknown model IDs are accepted, but context-window-based management is disabled for those models.

## Examples

```bash
# one-shot prompt
moa -p "fix flaky tests"

# explicit provider/model
moa -model openai/gpt-5.3-codex -p "optimize this query"

# disable sandbox + permissions (full yolo)
moa -yolo

# ask-mode permissions
moa -permissions ask

# auto-mode permissions with evaluator model override
moa -permissions auto -permissions-model haiku
```
