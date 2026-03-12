# CLI Reference

## Main command

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
-login              Login to provider: anthropic | openai | openai-transcribe
-logout             Remove stored credentials for provider
-output             text|json (default: text)
```

## Serve subcommand

```bash
moa serve [--host 127.0.0.1] [--port 8080] [--model sonnet]
```

### Serve flags

```text
--host   Bind address (default: 127.0.0.1)
--port   HTTP port (default: 8080)
--model  Default model for new web sessions
```

If you bind to anything other than localhost, Moa prints a warning because `moa serve` does not include built-in authentication.

`openai-transcribe` enables voice input in the web UI.

## Model selection

Accepted formats:

- alias: `sonnet`, `opus`, `haiku`, `codex`, `codex-spark`, `codex-5.2`
- canonical id: `claude-sonnet-4-6`
- provider/id: `anthropic/claude-sonnet-4-6`, `openai/gpt-5.3-codex`

Unknown model IDs are accepted, but context-window-based management is disabled for them.

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

# start web UI locally
moa serve

# enable voice input for the web UI
moa --login openai-transcribe

# expose web UI on the network
moa serve --host 0.0.0.0 --port 8080
```
