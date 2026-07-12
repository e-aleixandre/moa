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
- Per-session cost readout (main run + subagents), matching the TUI
- Rename (`/rename <title>`) and delete sessions from the overview
- Unread badges on sessions with activity you haven't seen yet
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

- **Images** (`jpeg/png/gif/webp`) are sent to the model natively as vision input. Large photos are downscaled in the browser before upload. The server validates the file's magic bytes against the declared type — a binary mislabeled as an image is saved to disk instead of being forwarded to the provider.
- **PDFs** are sent to the model natively as a `document` block **when the active provider supports it AND the bytes are actually a PDF** (`%PDF-` magic; Anthropic always supports documents, OpenAI on the API-key path). If the active provider does not support native documents — the PDF exceeds the size limit, or the content isn't a real PDF — it is saved to disk as a fallback (see below) and the agent is told where to find it. Because Moa is provider-agnostic and you can switch models mid-conversation, this decision is made per message against whichever provider is active at send time; a `document` already in the history is degraded to a text note if you later switch to a provider that can't accept it.
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
- Native PDFs are additionally capped at **24 MB per message** and **48 MB cumulative across the session's history** (because a native PDF is re-sent to the model every turn); PDFs beyond those caps fall back to disk instead.
- Files that exceed the client-side cap are rejected before upload. Raising these limits would require changing the transport (currently base64-in-JSON), which is out of scope.
- The base directory can be overridden with the `MOA_ATTACHMENTS_DIR` environment variable (default `/tmp/moa`).

## Security

By default `moa serve` has **no authentication** — anyone who can reach the port controls your agents. For access beyond `127.0.0.1`, pass `--token <secret>` (or set `MOA_SERVE_TOKEN`) to require a session cookie or `?token=<secret>` on every request; visiting that URL once sets an `HttpOnly` cookie for subsequent requests. An authenticated owner can additionally pair a revocable Pulse device. A claimed device authenticates REST and WebSocket requests with `Authorization: Moa-Device <device-id>.<secret>`; its credential is separate from the owner token and is rejected outside direct loopback unless the request uses TLS. Pairing and device credentials are not accepted in URLs.

The device credential is **not** a second owner token. Its legacy access is a
small explicit read-only allowlist (safe Ops/session/conversation/file
projections and read-only WebSocket streams); every other legacy route is
owner-only. In particular a device cannot call `/send`, `/instruction`,
permission, shell, command, config, branch, subagent, cancel, pairing, device
list, or revoke endpoints. New owner capabilities must get a typed Pulse
adapter rather than being added to this allowlist.

Moa also rejects requests whose `Host` header isn't `localhost`, an IP literal, or an explicit `--allowed-hosts` entry (anti DNS-rebinding), and requires an `X-Moa-Request` header on non-GET requests (CSRF protection). None of this replaces a real network boundary: prefer localhost, Tailscale, or a reverse proxy for remote access, and use `--token` on top of it. When pairing remotely, terminate TLS at Serve or a trusted proxy; Tailscale connectivity alone does not make an HTTP request TLS to Serve.

### Pulse typed write transactions

Paired Pulse devices can use a separate, device-only transaction surface:

- `POST /api/pulse/operations/prepare`
- `POST /api/pulse/operations/{id}/confirm` with an empty JSON object
- `GET /api/pulse/operations/{id}`

These routes require `Authorization: Moa-Device <device-id>.<secret>`; legacy
`--token` cookie/query authentication is deliberately rejected for them. They
also require normal Host validation, no query parameters, `X-Moa-Request` for
POSTs, strict JSON, and TLS unless Serve sees a direct loopback peer. They do
not alter the legacy web/TUI routes.

The supported kinds are `directed_instruction` and `permission_decision`.
`directed_instruction` accepts bounded `target` and `text`; prepare resolves an
exact Ops destination or returns `409` candidates.

`permission_decision` accepts only a bounded `target`, `decision`
(`approve_once` or `deny`), and optional bounded non-sensitive `feedback`.
The target must resolve to one session with exactly one current permission.
Prepare binds the private review to that session, the runtime permission ID,
run generation, tool, allow scope, and a canonical digest of raw arguments.
Confirm atomically revalidates that exact snapshot before using the canonical
one-off resolver. A changed/replaced request, new run, missing permission, or
legacy resolution is rejected safely. Its review contains only a bounded,
redacted target/tool and generic one-time scope; it never contains raw tool
arguments, tool output, permission IDs, or internal errors. `allow`, permanent
rules, `add_rule`, shell/command/config fields, and arbitrary decisions are
strict-schema errors. Permission reviews expire after two minutes; instruction
reviews expire after five.

Confirm binds the same paired device and immutable review; it never accepts a
client-supplied confirmation flag, endpoint/method, free-form command, new
text, or changed decision. Receipts are immutable and idempotent, retained for
one hour, and are never evicted early to make room for a newer operation:
admission returns `429` when pending or receipt capacity is full. An
instruction receipt says whether Moa accepted/rejected and delivered the
instruction; it does not claim that agent work is complete unless that is
separately observed. A permission receipt reports only `accepted`, `rejected`,
or `indeterminate` and whether permission resolution was observed; it never
claims completion of subsequent agent work. Raw permission args are never
persisted in the operation store. `status: "indeterminate"` and
`delivery: "indeterminate"` mean a crash or durable-storage window prevents
Moa from truthfully determining the result; Pulse must present that uncertainty and may
not treat it as rejection. Serve never retries such delivery in the background.
Instruction recovery consults its canonical ledger. Permission resolution has
no replay ledger: after its durable attempt marker, restart recovery is terminal
`indeterminate`, never a blind retry or approval. Revoke/expiry synchronously
invalidates pending operations and confirmation rechecks the active device at
the execution boundary.

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
