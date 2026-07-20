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
- Queue commands and messages while the agent is working (strict send order)
- Per-session cost readout (main run + subagents), matching the TUI
- Rename (`/rename <title>`) and delete sessions from the overview
- Unread badges on sessions with activity you haven't seen yet
- Multi-pane tiled layouts
- Keyboard-first navigation
- Voice input
- Pair a Pulse device by scanning a **QR code** (or manual code), created from the top bar (`POST /api/pulse/pairings`)
- **Version indicator** in the top bar that links to the latest release when an update is available

## Keyboard shortcuts

| Shortcut | Action |
|----------|--------|
| `⌘K` / `Alt+K` | Open session palette |
| `⌘1..9` / `Alt+1..9` | Focus pane by number |
| `⌘.` / `Alt+.` | Toggle voice input |
| `⌘O` / `Alt+O` | Toggle the session overview (mobile) |
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

### Files sent by the agent

The reverse direction is also supported: the agent can **send you a file** with the
`send_file` tool. It renders in the chat as a **download card** (name, size, type icon)
that fetches the file as a blob and hands it off via the OS share sheet on mobile or a
same-origin download on desktop. Files are served from a per-session, in-memory allowlist
(`GET /api/sessions/{id}/files/{fileID}`); the path never comes from the request, and the
descriptor is re-checked right before serving. Like disk attachments, these registrations
are in-memory only — they 404 after the session is deleted or the server restarts.

Text, Markdown and image files can be **previewed inline** in an embedded viewer. HTML files
get a live **preview rendered in a sandboxed `iframe`** (`sandbox="allow-scripts"`, no
`allow-same-origin`, plus a strict `Content-Security-Policy`), with an inspector button to
review any external resources the page references before it loads.

### Files saved to disk are ephemeral

- They live under `/tmp/moa/<session-id>/` and are **deleted when you delete the session**.
- They may **disappear if the server restarts** (`/tmp` is not durable). Resuming an old session does not restore them.
- "Attaching" a spreadsheet does **not** mean the model has read it — the agent must open it explicitly (e.g. via `bash`). Attached files are untrusted user data; the agent is told to treat them with care.

### Limits

- Up to **8 attachments** per message.
- **32 MB** per file; **64 MB** decoded total per message; **200 MB** on-disk per session.
- Native binary content (images, plus any natively-forwarded documents) is additionally capped at **48 MB cumulative across the session's history** (`maxSessionNativeDocBytes`), because native blocks are re-sent to the model every turn; individual images are capped at **5 MB** decoded. Content beyond the cumulative budget falls back to disk instead.
- Files that exceed the client-side cap are rejected before upload. Raising these limits would require changing the transport (currently base64-in-JSON), which is out of scope.
- The base directory can be overridden with the `MOA_ATTACHMENTS_DIR` environment variable (default `/tmp/moa`).

## Queued commands and mid-run messages

You don't have to wait for a run to finish before lining up your next move. What you type while the agent is working is handled in **strict send order** — the order you sent things is the order the agent sees them.

- **Messages** typed mid-run are *steered* onto a queue and delivered to the agent between steps of the current run (or, if they arrive after the run ends, they start the next one). They show up as a **queued** chip above the composer.
- **Slash commands** typed mid-run are classified by what they do:
  - **Queued** (`/compact`, `/clear`, `/model`, `/thinking`, `/verify`, `/goal <objective>`) — these rewrite or reconfigure the conversation, so they can't run in the middle of a live turn. They wait in the queue as a **command** chip and run at the next idle point, in order relative to your messages. So `message → /compact → message` compacts *after* the first message lands and *before* the second.
  - **Instant** (`/rename`, `/permissions`, `/path`, `/tasks`, `/schedule`, `/goal status`, `/goal stop`) — these only touch side state, so they run immediately without waiting.
  - **Rejected** (`/undo`, `/branch`, `/back`, `/plan`) — these only make sense against a settled conversation and are rejected while the agent is working (the `reject` queue policy); stop the run first.
- **Attachments** can be added to a mid-run message too (the paperclip is no longer disabled while a run is in flight); the image/file rides along with the steered message.
- **Editing the queue**: click the queued chip (or `Alt+↑`) to pull everything back into the composer for editing — this cancels the not-yet-delivered items so you don't get both the originals and your edit. Queued images can't be pulled back (only their count is tracked client-side), so re-attach them if needed.
- **Stopping**: pressing Stop/`Esc` while a run is in flight dumps whatever was queued back into the composer, so nothing you lined up is silently lost.

`/clear` while a run is queued behind it starts a fresh conversation but keeps the items queued after it — they belong to the new conversation.

## Security

By default `moa serve` has **no authentication** — anyone who can reach the port controls your agents. For access beyond `127.0.0.1`, pass `--token <secret>` (or set `MOA_SERVE_TOKEN`) to require a session cookie or `?token=<secret>` on every request; visiting that URL once sets an `HttpOnly` cookie for subsequent requests. The owner boundary is therefore either that token, when configured, or the operator-selected network boundary (localhost/Tailscale) when it is not. That owner can pair a revocable Pulse device. A claimed device authenticates REST and WebSocket requests with `Authorization: Moa-Device <device-id>.<secret>`; its credential is separate from the owner token and is rejected outside direct loopback unless the request uses TLS. Pairing and device credentials are not accepted in URLs.

When a normal OpenAI API key is configured for auxiliary features (the
`openai-transcribe` credential, set with `moa --login openai-transcribe`, or a
plain `OPENAI_API_KEY` / API-key `openai` credential — never OpenAI OAuth), a
paired device
may call `POST /api/pulse/realtime/client-secret` with exactly `{}` to receive a
Realtime client secret requested for 60 seconds (Moa accepts at most an additional
5 seconds for OpenAI clock/transport skew). This is a device-only route: owner cookies and
tokens cannot mint it. Moa sends only the server-controlled `gpt-realtime-2.1`
Realtime configuration to OpenAI and returns a minimal credential DTO; Pulse then talks directly to
OpenAI. Moa does not proxy, store, or log audio, SDP, conversation data, the
client secret, or the permanent API key. Revocation prevents a subsequent mint
from being delivered once it wins the device lifecycle boundary; it cannot recall
a client secret already delivered, which may remain usable until its OpenAI expiry.
The route has the same Host, CSRF,
TLS/loopback, revocation, concurrency, and rate-limit protections as other
paired-device operations.

An emparejado Pulse device represents the owner on Serve's **generic API**:
it can read sessions, conversations and activity and use the same generic
actions as the web client. This is deliberate: Pulse is a client of Moa, not a
separate restricted product surface. The exceptions are pairing administration:
only the network/token owner can create pairings, list paired devices or revoke
a device. An already paired device cannot extend its own authority.

## Attention queue and permission decisions

`GET /api/attention` returns an informational, cross-session snapshot of
unresolved attention items. It describes what needs the owner's attention; it
does not define an approval or echo-confirmation protocol. For a permission
item, an owner-authorized client uses its `session_id` and `ref_id` with the
existing generic `POST /api/sessions/{id}/permission` action to decide it.

Permission items retain `risk_level`, `risk_flags`, and `verbatim` so a client
can present or read the assessed risk and exact command before making that
generic decision. They are information for the client and owner, not a
server-enforced confirmation ceremony. The attention item intentionally no
longer includes `requires_verbatim_confirm`; clients must not infer an
echo-confirmation requirement from the queue. Serve has no formal API version;
this is the current attention contract.

Moa also rejects requests whose `Host` header isn't `localhost`, an IP literal, or an explicit `--allowed-hosts` entry (anti DNS-rebinding), and requires an `X-Moa-Request` header on non-GET requests (CSRF protection). None of this replaces a real network boundary: prefer localhost, Tailscale, or a reverse proxy for remote access, and use `--token` on top of it. When pairing remotely, terminate TLS at Serve or a trusted proxy; Tailscale connectivity alone does not make an HTTP request TLS to Serve.

## REST endpoints

Beyond the per-session WebSocket, Serve exposes a few global read/write endpoints:

| Endpoint | Purpose |
|----------|---------|
| `GET /api/version` | Current version and whether an update is available |
| `GET /api/capabilities` | Server/session capabilities (providers, features) |
| `GET /api/usage` | Usage/cost readout |
| `GET /api/sessions/{id}/files` · `GET /api/sessions/{id}/files/{fileID}` | List and download files the agent shared via `send_file` |
| `POST /api/pulse/pairings` · `.../pairings/claim` · `GET /api/pulse/devices` · `POST /api/pulse/devices/{id}/revoke` | Pulse pairing and device administration (owner-only) |
| `GET /api/push/vapid-public-key` · `POST /api/push/subscribe` · `.../unsubscribe` | Web-push subscription management |

## Frontend development

The web UI is the **redesigned frontend** in `pkg/serve/frontend-next/` (Preact),
served at `/` (and, for an already-installed PWA, still at `/next/`). Build it,
then override the embedded output for live development:

```bash
# build the SPA into pkg/serve/static-next (embedded at compile time)
cd pkg/serve/frontend-next && node esbuild.mjs   # or: bun esbuild.mjs

# serve that build directory without recompiling the binary
MOA_SERVE_STATIC_NEXT_DIR=pkg/serve/static-next moa serve
```

`MOA_SERVE_STATIC_DIR` no longer overrides the interface at `/` — after the
cutover the old SPA is retired and that tree only provides the shared root
assets the frontend references absolutely (the service worker `/sw.js` and the
PWA icons).

<p align="center">
  <img src="./assets/serve-desktop-overview.png" alt="Desktop" width="900" />
</p>

<p align="center">
  <img src="./assets/serve-mobile-session.png" alt="Mobile" width="320" />
</p>
