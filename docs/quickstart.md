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

### go install

```bash
go install github.com/e-aleixandre/moa/cmd/moa@latest
```

Builds from source, so you need Go 1.25+. Only works for releases newer than
v0.18 (older tags predate the current module path).

### Keeping it up to date

```bash
moa update --check   # report the available version
moa update           # download, verify, and replace the binary
```

Binaries installed by Homebrew or Nix are left to their package manager: `moa
update` refuses to touch them and tells you the command to run instead. Updates
never restart anything — restart Moa yourself afterwards.

## Requirements

- A provider login: an API key (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or `XAI_API_KEY`) or an interactive OAuth login (see [Authenticate](#authenticate)) — at least one provider
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
# Or use the separately metered xAI developer API
export XAI_API_KEY="..."
# Then select Grok
moa -model grok
```

### OAuth / interactive login

```bash
moa --login anthropic
moa --login openai
# SuperGrok/X subscription (uses the Grok consumer proxy, not XAI_API_KEY billing)
moa --login xai
```

`XAI_API_KEY` uses the metered `api.x.ai` developer API. It is separate from a
SuperGrok/X subscription. `moa --login xai` instead starts an OAuth device flow:
open the displayed verification URL, enter the code, and sign in with the X
account that has the applicable SuperGrok/X entitlement. This consumer route
uses a shared public Grok Build client and a private consumer proxy, so it is
best-effort rather than a promised public xAI API integration. If both are set,
`XAI_API_KEY` takes precedence over the stored xAI OAuth login.

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
