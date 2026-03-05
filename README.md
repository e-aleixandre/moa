# go-agent

A minimal, extensible coding agent in Go. Primitives, not features.

## V0 — Headless Mode

```bash
# Run with a prompt
agent -p "refactor the handler to use middleware" --model claude-sonnet-4-20250514

# From file
agent -p @prompt.md --model claude-sonnet-4-20250514 --thinking medium

# From stdin
echo "fix the tests" | agent --model claude-sonnet-4-20250514
```

## Build

```bash
make build   # → bin/agent
make test    # go test -race ./...
make vet     # go vet ./...
```

## Architecture

```
pkg/core/       Shared types (no import cycles)
pkg/agent/      Agent loop + event emitter
pkg/provider/   LLM provider abstraction (Anthropic V0)
pkg/tool/       Tool registry + built-in tools
pkg/extension/  Extension API + typed hooks
pkg/context/    AGENTS.md loading + system prompt
cmd/agent/      CLI entrypoint
```

## Requirements

- Go 1.22+
- `ANTHROPIC_API_KEY` environment variable
