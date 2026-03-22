# Quickstart

## Requirements

- Go 1.25+
- At least one provider key: `ANTHROPIC_API_KEY` or `OPENAI_API_KEY`

## Build

```bash
make build
# → ./bin/moa
```

> Examples below use `moa`. If you built locally without installing, use `./bin/moa`.

## Authenticate

### Environment variables (simplest)

```bash
export ANTHROPIC_API_KEY="..."
# or
export OPENAI_API_KEY="..."
```

### OAuth / interactive login

```bash
moa --login anthropic
moa --login openai
```

For voice input (TUI and web UI):

```bash
moa --login openai-transcribe
```

Remove credentials:

```bash
moa --logout anthropic
```

## Use it

```bash
# Interactive TUI
moa

# One-shot
moa -p "refactor the handler to use middleware"
moa -p @prompt.md

# Web UI
moa serve
# → http://127.0.0.1:8080
```

## Resume sessions

```bash
moa --continue       # latest session
moa --resume         # session browser
moa --resume <id>    # specific session
```

## Next

- [CLI Reference](./cli.md) — all flags and model aliases
- [TUI Usage](./tui.md) — slash commands, keybindings, plan mode
- [Web UI](./serve.md) — `moa serve` features
- [Configuration](./configuration.md) — config files and options
