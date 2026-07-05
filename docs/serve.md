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

## Attachments

The composer accepts file attachments (paperclip icon, drag-and-drop, or paste). How each file is handled depends on its type:

- **Images** (`jpeg/png/gif/webp`) are sent to the model natively as vision input. Large photos are downscaled in the browser before upload.
- **PDFs** are sent to the model natively as a `document` block **when the active provider supports it** (Anthropic always; OpenAI on the API-key path). If the active provider does not support native documents — or the PDF exceeds the size limit — the PDF is saved to disk as a fallback (see below) and the agent is told where to find it. Because Moa is provider-agnostic and you can switch models mid-conversation, this decision is made per message against whichever provider is active at send time.
- **Small UTF-8 text** (≤256 KiB: `.txt/.md/.csv/.json`, source code, etc.) is inlined directly into the message, wrapped in an `<attachment>` marker.
- **Everything else** (`.xlsx/.docx/.zip`, binaries, and text larger than 256 KiB) is **saved to disk** under `/tmp/moa/<session-id>/`, and that directory is added to the session's path allowlist so the agent can process the file with its own tools (`bash`, `read_file`, etc.). Moa itself does not parse Office/archive formats — the agent decides how, on demand.

In the conversation history, each attachment is tagged so you can tell which path it took: **enviado al modelo** (native image/PDF), a collapsible inline chip, or **guardado en disco** (with the on-disk path).

### Files saved to disk are ephemeral

- They live under `/tmp/moa/<session-id>/` and are **deleted when you delete the session**.
- They may **disappear if the server restarts** (`/tmp` is not durable). Resuming an old session does not restore them.
- "Attaching" a spreadsheet does **not** mean the model has read it — the agent must open it explicitly (e.g. via `bash`). Attached files are untrusted user data; the agent is told to treat them with care.

### Limits

- Up to **8 attachments** per message.
- **32 MB** per file; **64 MB** decoded total per message; **200 MB** on-disk per session.
- Files that exceed the client-side cap are rejected before upload. Raising these limits would require changing the transport (currently base64-in-JSON), which is out of scope.
- The base directory can be overridden with the `MOA_ATTACHMENTS_DIR` environment variable (default `/tmp/moa`).

## Security

By default `moa serve` has **no authentication** — anyone who can reach the port controls your agents. For access beyond `127.0.0.1`, pass `--token <secret>` (or set `MOA_SERVE_TOKEN`) to require a session cookie or `?token=<secret>` on every request; visiting that URL once sets an `HttpOnly` cookie for subsequent requests. Moa also rejects requests whose `Host` header isn't `localhost`, an IP literal, or an explicit `--allowed-hosts` entry (anti DNS-rebinding), and requires an `X-Moa-Request` header on non-GET requests (CSRF protection). None of this replaces a real network boundary: prefer localhost, Tailscale, or a reverse proxy for remote access, and use `--token` on top of it.

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
