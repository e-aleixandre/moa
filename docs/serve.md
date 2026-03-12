# Serve / Web UI

`moa serve` starts a small HTTP/WebSocket server and exposes Moa in the browser.

It is useful when you want to:

- continue sessions from another device
- use Moa from mobile more comfortably than a raw terminal
- keep multiple browser sessions open at once

## Start it

```bash
moa serve
```

Default address:

```text
http://127.0.0.1:8080
```

Expose it on your network:

```bash
moa serve --host 0.0.0.0 --port 8080
```

## What it supports today

- multiple sessions
- per-session working directory
- session persistence and resume
- streaming output over WebSocket
- permission prompts
- cancel running sessions
- subagents
- MCP loading per session
- model and thinking reconfiguration

## Security note

`moa serve` does **not** include built-in authentication.

Safe default use cases:

- localhost
- Tailscale or another private network
- behind your own reverse proxy with auth

## Static assets

By default, the web UI is served from embedded assets.

For frontend development, you can override the static directory:

```bash
MOA_SERVE_STATIC_DIR=/path/to/static moa serve
```

> Insertar imagen de la vista web con lista de sesiones
>
> Insertar imagen de la misma sesión abierta en móvil
