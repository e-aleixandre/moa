<p align="center">
  <img src="logo.png" width="200" alt="Moa logo" />
</p>

<h1 align="center">Moa</h1>
<p align="center"><strong>My Own Agent</strong> — A minimal, extensible coding agent in Go.</p>

---

## Quick Start

```bash
# With API key
ANTHROPIC_API_KEY=sk-ant-... moa -p "refactor the handler to use middleware"

# With Claude Max (OAuth)
moa --login
moa -p "fix the tests"

# From file or stdin
moa -p @prompt.md --thinking medium
echo "explain this code" | moa
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
pkg/provider/   LLM provider abstraction (Anthropic)
pkg/tool/       Built-in tools (bash, read, write, edit, grep, find, ls)
pkg/extension/  Extension system with typed hooks
pkg/auth/       Credential store + OAuth PKCE flow
pkg/context/    AGENTS.md loading + system prompt
cmd/agent/      CLI entrypoint
```

## Requirements

- Go 1.22+
- `ANTHROPIC_API_KEY` environment variable, or `moa --login` for Claude Max
