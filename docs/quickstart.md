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

Updates never restart anything — restart Moa yourself afterwards. See
[CLI Reference](./cli.md#update-subcommand) for the details and the Homebrew/Nix
caveat.

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

Moa needs credentials for at least one provider. They are stored in
`~/.config/moa/auth.json` (mode `0600`), or under `MOA_CONFIG_DIR` if you set
it. An environment variable always wins over a stored credential for the same
provider.

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

**Anthropic** (Claude Pro/Max) and **OpenAI** (ChatGPT Plus/Pro) use the same
browser flow: moa opens the provider's authorization page (and prints the URL if
the browser does not open), you sign in, and the provider hands back a callback
URL. Paste that whole URL — or just the `code#state` fragment, or the bare code
— at the `Paste callback URL, code#state, or code here:` prompt. On success the
credential is written to `auth.json`.

`moa --login openai` first asks which method you want: `1` for the ChatGPT
subscription OAuth flow above, `2` to paste a plain OpenAI API key instead (read
without echo).

`XAI_API_KEY` uses the metered `api.x.ai` developer API. It is separate from a
SuperGrok/X subscription. `moa --login xai` instead starts an OAuth device flow:
open the displayed verification URL, enter the code, and sign in with the X
account that has the applicable SuperGrok/X entitlement. This consumer route
uses a shared public Grok Build client and a private consumer proxy, so it is
best-effort rather than a promised public xAI API integration. If both are set,
`XAI_API_KEY` takes precedence over the stored xAI OAuth login.

An expired login does not lock you out of Serve: sessions still open and
existing ones still reopen, and the failure appears when you send a message,
naming the provider to sign in to again. Switching that session to a model from
a provider that is still authenticated is enough to keep working. A one-shot CLI
run reports the problem before it starts, since it has nothing to fall back to.

For voice input in the web UI (and Pulse Realtime), store a plain OpenAI API key
in a separate slot, so the agent itself can stay on an OpenAI OAuth
subscription:

```bash
moa --login openai-transcribe
```

Remove credentials:

```bash
moa --logout anthropic
```

## Use it

```bash
# One-shot
moa -p "refactor the handler to use middleware"
moa -p @prompt.md

# Web UI
moa serve
# → http://127.0.0.1:8080
```

### What the first `moa serve` needs

- **Credentials for one provider**, as above. Serve opens sessions even without
  them; the error appears when you send the first message.
- **A working directory.** `moa serve` uses the directory you start it in as the
  workspace root, and every new session defaults to it. Start it in the
  repository you want to work on, or pick another path per session in the
  session palette (`⌘K`).
- **Nothing else.** Config files are optional: with no `~/.config/moa/config.json`
  moa runs in its default `yolo` permission mode with an unrestricted path
  scope. Tighten that with [Configuration](./configuration.md#permissions).
- **No authentication is on by default,** and Serve binds to `127.0.0.1`. Before
  exposing it anywhere else, read [Web UI → Security](./serve.md#security).

Files moa creates on first use, all under `~/.config/moa/` (or `MOA_CONFIG_DIR`):
`auth.json` for credentials, `sessions/` for history, `config.json` if you write
one. See [Overview → Storage](./overview.md#storage) for the full list.

## Troubleshooting

**`no credentials for provider "x": set X_API_KEY or run --login`** — nothing is
stored for the provider of the model you asked for. Set the environment variable
or run the matching `moa --login`.

**`token refresh failed: … (run --login <provider> to re-authenticate)`** — the
stored OAuth credential expired and the refresh was rejected (`invalid_grant`
and similar come from the provider). Run `moa --login <provider>` again. In
Serve you can keep working meanwhile by switching the session to a model from a
provider that is still authenticated.

**Anthropic rejects OAuth requests, complaining the Claude Code version is too
old** — Anthropic requires OAuth clients to announce a recent Claude Code client
version, and raises the floor over time. Moa hardcodes that version, so the fix
is to update moa (`moa update`); nothing in your configuration affects it.

**An MCP server never becomes ready** — sessions no longer wait for the MCP
handshake, so a server that fails to start shows up afterwards as failed in the
session's MCP panel, and its tools are simply absent. The server's own error is
reported there; the handshake has a 15-second timeout. Enable, disable or
restart it from that panel. For a project `.mcp.json`, also check the directory
is trusted — an untrusted project's servers are not loaded at all (see
[Configuration](./configuration.md#project-directory-moa)).

**Voice input does nothing** — it needs `moa --login openai-transcribe` (or a
plain `OPENAI_API_KEY`), and browsers only grant microphone access over HTTPS or
on localhost.

## Next

- [CLI Reference](./cli.md) — all flags and model aliases
- [Web UI](./serve.md) — `moa serve` features
- [Configuration](./configuration.md) — config files and options
