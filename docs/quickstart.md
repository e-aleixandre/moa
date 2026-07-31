# Quickstart

## Install

### Install script (Linux, macOS)

```bash
curl -fsSL https://letmoa.run/install.sh | sh
```

Installs the latest release into `/usr/local/bin` if it is writable, otherwise
`~/.local/bin`. Override with `MOA_INSTALL_DIR`, or pin a version with
`MOA_VERSION=v0.18.1`. The script never uses `sudo`.

### Homebrew (macOS, Linux)

```bash
brew install e-aleixandre/tap/moa
```

### Manual download

Grab the archive for your platform from
[GitHub Releases](https://github.com/e-aleixandre/moa/releases/latest), verify it
against `checksums.txt`, extract it, and put `moa` somewhere on your `PATH`.

### Keeping it up to date

```bash
moa update --check   # report the available version
moa update           # download, verify, and replace the binary
```

Binaries installed by Homebrew or Nix are left to their package manager: `moa
update` refuses to touch them and tells you the command to run instead. Updates
never restart anything — restart Moa yourself afterwards.

## Requirements

- A provider login: either an API key (`ANTHROPIC_API_KEY` / `OPENAI_API_KEY`) or an interactive OAuth login (see [Authenticate](#authenticate)) — at least one of Anthropic or OpenAI
- To build from source instead of installing a release: Go 1.25+ and Node.js/npm (the latter only to build the embedded web UI frontend via `make build`)

## Build from source

```bash
make fe-install   # first time only: install frontend deps
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
