# Automation API

Moa is reachable from the outside the same way it reaches everything else: with a
small, generic contract. An external system (a webhook, a cron job, a CI step)
starts a **normal session** and sends it a prompt. There is no separate "job"
type — the resulting session has the same runtime, permissions, persistence and
push notifications as one you started yourself, and you can open it on your
phone and keep talking mid-run.

Concrete integrations (Linear, GitHub, Jira…) are recipes written on top of this
contract, not features of the core.

## Enabling it

The Automation API has its **own** shared secret, separate from the browser
`--token`:

```bash
moa serve --automation-token "$(openssl rand -hex 32)"
# or
MOA_AUTOMATION_TOKEN=… moa serve
```

The flag wins over the environment variable. **Without a configured token the
automation routes do not exist** — they answer `404`, even on localhost. This is
deliberate: the surface is fail-closed, so an unattended machine never grows an
inbound API by accident.

## Authentication

Automation requests present the token in the `Authorization` header:

```
Authorization: Bearer <automation-token>
```

- The comparison is constant time, and the token is **never** accepted in a URL
  query.
- Bearer requests are exempt from the `X-Moa-Request` CSRF header the browser
  client sends: a cross-site form cannot set an `Authorization` header, so the
  header check adds nothing here.
- They are **not** exempt from the Host check. DNS rebinding protection applies
  as everywhere else, so a named host still needs `--allowed-hosts`.
- The two surfaces do not overlap: the automation token grants access to
  `/api/automation/…` and nothing else, and a browser cookie or a paired Pulse
  device cannot call the automation routes. Permission decisions, cancel and
  shell stay with the human, on the UI they already use.

## `POST /api/automation/runs`

Create a session and send it a first prompt in a single call.

Request body:

| Field | Required | Description |
|-------|----------|-------------|
| `prompt` | yes | First message for the agent |
| `model` | no | Model spec (e.g. `sonnet`, `openai/gpt-5`); defaults to the server's default |
| `cwd` | no | Working directory; defaults to the server workspace root |
| `title` | no | Session title (treated as manually set, so auto-titling won't overwrite it) |
| `origin` | no | Free-form label for the caller, e.g. `linear-webhook`; defaults to `automation` |
| `idempotency_key` | no | Deduplicates retries — see below |
| `callback_url` | no | Absolute `http`/`https` URL to notify when the run settles |
| `callback_secret` | no | Shared secret for signing that future callback |

```bash
curl -sS http://127.0.0.1:8080/api/automation/runs \
  -H "Authorization: Bearer $MOA_AUTOMATION_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
        "prompt": "Fix the failing test in pkg/serve and open a PR",
        "origin": "linear-webhook",
        "idempotency_key": "LIN-4821",
        "callback_url": "https://hooks.example.com/moa"
      }'
```

Response `201 Created`:

```json
{
  "session_id": "s_01H…",
  "url": "http://127.0.0.1:8080/?session=s_01H…",
  "created": true
}
```

`url` is derived from the `Host` of your request, so it is an address that
whoever called Moa can actually open — tap it on your phone and you are in the
session while the agent is still working.

Errors: `400` for a missing/blank prompt, an unknown model, an invalid `cwd` or a
`callback_url` that is not an absolute http(s) URL; `401` for a missing or wrong
bearer token; `404` when the API is not enabled.

## Idempotency

Webhooks redeliver. Pass an `idempotency_key` and a repeat call returns the
session that key already created:

- First call → `201 Created`, `"created": true`.
- Any repeat with the same key → `200 OK`, `"created": false`, the **same**
  `session_id`, and the prompt is **not** sent again.

The key is stored in the session's metadata, and the index is rebuilt from disk
at startup, so deduplication survives a restart. Deleting the session releases
its key: a later retry then starts a fresh run.

Without a key, every call creates a new session.

## Origin

Every session records who created it under metadata `origin`. Sessions you start
yourself are `user` (stored implicitly: no key means user), and automation
sessions carry `automation` or whatever label the caller passed. It shows up in
session summaries (`GET /api/sessions`) as `origin`, omitted for ordinary user
sessions.

## Permissions and unattended runs

An automated session inherits the normal permission configuration — it is not
more dangerous because it arrived over a webhook. If the agent needs a decision
(a permission prompt or `ask_user`), the run waits and the usual attention push
reaches your phone, where you decide as always.

## Callbacks (future release)

`callback_url` and `callback_secret` are validated and stored today, but **no
callback is delivered yet**. Outbound delivery when a run goes quiescent —
`done` / `failed` / `needs_input`, with a short summary and a link to the
session, plus optional HMAC signing — lands in a following release. Until then,
poll `GET /api/sessions/{id}` (with the browser credential) or just watch the
session from the UI.
