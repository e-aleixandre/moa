# Quickstart

## Requirements

- Go `1.25+`
- Credentials for at least one provider:
  - `ANTHROPIC_API_KEY`
  - `OPENAI_API_KEY`
  - or OAuth login via CLI

## Build

```bash
make build
# binary: ./bin/moa
```

> The examples below use `moa` as the command name. If you build locally and have not installed it, use `./bin/moa`.

## Authenticate

### Environment variables

```bash
export ANTHROPIC_API_KEY="..."
# and/or
export OPENAI_API_KEY="..."
```

### OAuth login

```bash
moa --login anthropic
moa --login openai
```

Optional, for voice input in the web UI:

```bash
moa --login openai-transcribe
```

Browser microphone access usually requires HTTPS, so voice input works best on localhost, Tailscale, or behind your own HTTPS setup.

Remove stored credentials:

```bash
moa --logout anthropic
moa --logout openai
moa --logout openai-transcribe
```

## Use Moa

### Interactive TUI

```bash
moa
```

### Headless CLI

```bash
moa -p "refactor the handler to use middleware"
moa -p @prompt.md
printf "explain this package" | moa
```

### Web UI

```bash
moa serve
```

Then open:

```text
http://127.0.0.1:8080
```

For remote access, bind explicitly:

```bash
moa serve --host 0.0.0.0 --port 8080
```

> `moa serve` has no built-in authentication. Only expose it on localhost, a private network, or behind your own auth layer.

## Resume sessions

```bash
moa --continue      # latest session
moa --resume        # open session browser
moa --resume <id>   # open specific session
```

## Next

- [CLI Reference](./cli.md)
- [Serve / Web UI](./serve.md)
- [TUI Usage](./tui.md)
- [Configuration](./configuration.md)
