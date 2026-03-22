# Web UI

`moa serve` starts an HTTP/WebSocket server that exposes Moa in the browser.

```bash
moa serve                              # http://127.0.0.1:8080
moa serve --host 0.0.0.0 --port 8080   # expose on network
```

## Features

- Multiple concurrent sessions with per-session working directory
- Session persistence and resume
- Streaming output over WebSocket
- Permission prompts and cancel
- Plan mode, subagents, MCP
- Model and thinking reconfiguration per session
- Multi-pane tiled layouts
- Keyboard-first navigation
- Voice input

## Keyboard shortcuts

| Shortcut | Action |
|----------|--------|
| `⌘K` / `Alt+K` | Open session palette |
| `⌘1..9` / `Alt+1..9` | Focus pane by number |
| `⌘.` / `Alt+.` | Toggle voice input |
| `Esc` | Close palette / go back |

On non-Mac platforms, `Alt` replaces `⌘` to avoid browser shortcut conflicts.

## Session palette

The session palette (`⌘K`) lets you search sessions, jump to open ones, resume saved sessions, or create new ones with a chosen project path.

## Panes

On desktop, you can split panes horizontally or vertically, switch focus by keyboard, and apply layout presets from the top bar.

## Voice input

Requires `moa --login openai-transcribe`. Browser microphone access usually needs HTTPS, so it works best on localhost, Tailscale, or behind your own HTTPS setup.

## Security

`moa serve` has **no built-in authentication**. Use it on localhost, a private network, or behind a reverse proxy with auth.

## Frontend development

Override embedded assets for live development:

```bash
MOA_SERVE_STATIC_DIR=pkg/serve/frontend/src moa serve
```

<p align="center">
  <img src="./assets/serve-desktop-overview.png" alt="Desktop" width="900" />
</p>

<p align="center">
  <img src="./assets/serve-mobile-session.png" alt="Mobile" width="320" />
</p>
