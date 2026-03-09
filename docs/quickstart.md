# Quickstart

## Requirements

- Go `1.25+`
- Credentials for at least one provider:
  - Anthropic (`ANTHROPIC_API_KEY`) or
  - OpenAI (`OPENAI_API_KEY`) or
  - OAuth login via CLI

## Build

```bash
make build
# binary: ./bin/agent
```

> The examples below use `moa` as command name. If you build locally, use `./bin/agent` unless you rename/install it.

## Authenticate

### Environment variables

```bash
export ANTHROPIC_API_KEY="..."
# and/or
export OPENAI_API_KEY="..."
```

### OAuth / interactive login

```bash
moa --login anthropic
moa --login openai
```

Remove stored credentials:

```bash
moa --logout anthropic
moa --logout openai
```

## Run

### Interactive TUI

```bash
moa
```

### Headless

```bash
moa -p "refactor the handler to use middleware"
moa -p @prompt.md
printf "explain this package" | moa
```

## Resume sessions

```bash
moa --continue      # latest session
moa --resume        # open session browser
moa --resume <id>   # open specific session
```

## Next

- [CLI flags](./cli.md)
- [TUI usage](./tui.md)
- [Configuration](./configuration.md)
