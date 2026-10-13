# moa native push — wire protocol v1

Three parties: the **phone app** (iOS with its Notification Service Extension,
or Android), the user's own **moa server**, and the **relay** (this Worker). The
relay holds the APNs key and the FCM service account; it never sees
notification content and keeps no state.

Two push **providers** exist: `apns` (iOS, the default) and `fcm` (Android,
Firebase Cloud Messaging HTTP v1). Everything below describes `apns` unless a
section says otherwise; the FCM differences are collected in
[Provider `fcm`](#provider-fcm). The `provider` field is additive: a client that
never sends it gets exactly the original behaviour.

Every byte-level rule below is pinned by `test/vectors.json`, which the Go
server (`pkg/push`), the relay tests and the iOS implementation must all
reproduce.

## Encoding

- `b64u(x)`: base64url **without padding** (RFC 4648 §5). Every binary field on
  the wire uses it.
- `‖` is concatenation; `0x00` is a single zero byte; labels are their UTF-8
  (ASCII) bytes.
- Times are integer Unix seconds.
- `HKDF(ikm, info, L)`: HKDF-SHA256 (RFC 5869) with an **empty salt**, `info`
  the label bytes, output `L` bytes.
- `HMAC(k, m)`: HMAC-SHA256, 32-byte output.
- `GCM(k, nonce, pt, aad)`: AES-256-GCM, 12-byte random nonce, 16-byte tag,
  output `ct ‖ tag`.

## Device keys

When the user turns native notifications on (and again after re-pairing) the
iPhone draws a 32-byte secret `S` from a CSPRNG and derives:

| Key | Derivation | Used by |
|---|---|---|
| `K_enc` | `HKDF(S, "moa-push-enc-v1", 32)` | server encrypts, NSE decrypts |
| `K_send` | `HKDF(S, "moa-push-send-v1", 32)` | authorizes sends to this device |
| `K_col` | `HKDF(S, "moa-push-collapse-v1", 32)` | opaque grouping ids |
| `kid` | `HKDF(S, "moa-push-kid-v1", 8)` | names the key in the envelope |

`S` goes only to the user's moa server, over the device-authenticated channel.
The relay receives `K_send` (sealed into the handle), never `S`, `K_enc` or
`K_col`.

## Relay keys

The relay has one secret `KRELAY` (32 random bytes, `b64u` in the Worker
secret). It derives `K_handle = HKDF(KRELAY, "moa-relay-handle-v1", 32)` and
`K_reg = HKDF(KRELAY, "moa-relay-reg-v1", 32)`. The APNs key is a separate
secret.

### Sealed blobs (challenge and handle)

```
sealed = b64u( rk ‖ nonce(12) ‖ GCM(K, nonce, json, label ‖ rk) )
rk     = 0x01                      (key version byte)
json   = {"t":<token>,"e":<env>,"k":b64u(K_send),"x":<expiry>}
```

| Blob | `K` | `label` | expiry |
|---|---|---|---|
| challenge `c` | `K_reg` | `"moa-reg-v1"` | now + 300 s |
| handle `h` | `K_handle` | `"moa-handle-v1"` | now + 90 days |

`token` is the APNs device token in lowercase hex (`^[0-9a-f]{64,200}$`);
`env` is `"sandbox"` (Xcode builds) or `"production"` (TestFlight/App Store).
Only the relay that sealed a blob can open it.

For `fcm` the json gains a fifth field, written last and only for `fcm`:
`{"t":…,"e":"production","k":…,"x":…,"p":"fcm"}`. Absent `p` means `apns`, so
blobs sealed before FCM existed open identically. `p` is inside the AEAD, so
the provider of a handle or challenge cannot be changed. A blob with any other
shape (`p:"apns"`, unknown `p`, token not matching its provider) is
`401 unauthorized`.

## 1. Registration (iPhone ↔ relay)

1. `POST /v1/register` with `{"token","env","send_key":b64u(K_send)}` and
   optionally `"provider":"apns"|"fcm"` (absent = `apns`; any other value is
   `400`; the schema stays closed).
   The relay returns **no handle**. It sends the token a challenge
   notification and answers `202 {"status":"sent"}`:

   ```json
   {"aps":{"alert":{"loc-key":"PUSH_CHALLENGE_BODY"},"interruption-level":"passive"},"r":"<c>"}
   ```

   (`apns-push-type: alert`, `apns-priority: 10`, `apns-expiration: now+300`.)
   At most 3 challenges per destination per 60 s.

2. The app receives it in the foreground (`willPresent`, not shown). It answers
   **only** if it started a registration itself in the last 5 minutes, for
   this token and env, with this `K_send`:

   `POST /v1/confirm` with `{"c":<c>,"p":b64u(HMAC(K_send, "moa-confirm-v1" ‖ 0x00 ‖ ascii(c)))}`

3. The relay opens `c`, checks its expiry, verifies `p` against the sealed
   `K_send` and answers
   `200 {"handle":<h>,"expires_at":<unix>,"token":<token>,"env":<env>,"provider":<provider>}`
   (`provider` is always present, `"apns"` for the original flow):
   the destination `c` was issued for. The app accepts the handle only if
   they are the token and env of the registration it is waiting on; a
   challenge from an earlier registration (another token, still unexpired)
   is ignored.

A third party who knows the token can register its own `K_send`, but the
challenge reaches the real iPhone, which did not ask for it and never confirms.

## 2. Server registration (iPhone ↔ its moa server)

`POST /api/push/native`, authenticated **only** by the native header
`Authorization: Moa-Device <credential>` (a WebView cookie is refused):

```json
{"relay_url":"https://push.letmoa.run","handle":"<h>","expires_at":<unix>,"secret":"<b64u(S)>","env":"production"}
```

The server accepts it only if `relay_url` equals its configured
`push_relay_url`. `DELETE /api/push/native` removes it;
`GET /api/push/native/status` reports it without secrets. Revoking or expiring
the device removes it.

## 3. Envelope (server → NSE, end to end)

Plaintext, UTF-8 JSON, then padded with spaces (0x20) to **exactly 2048 bytes**:

```json
{"v":1,"id":"<b64u 16 random bytes>","exp":<t+43200>,"d":"session|inbox|home","s":"<session id, only with d=session>","k":"ask|permission|done|failed|digest|event","lvl":"urgent|active|passive","th":"<thread>","t":"<title>","b":"<body>"}
```

- `s` matches `^[A-Za-z0-9_-]{1,128}$`; anything else becomes `d:"home"`.
- `t` and `b` are cut on UTF-8 boundaries until the JSON fits.
- `th` is the collapse id of the group (opaque, stable per group).

```
envelope = {"v":1,"k":b64u(kid),"c":b64u(nonce ‖ GCM(K_enc, nonce, padded, "moa-push-v1" ‖ kid))}
```

`envelope.c` is always 2768 characters and `envelope.k` 11.

The NSE authenticates **before** reading anything: unknown `v`, a `kid` that is
not the current one, a failed tag, an unknown `v` inside, or `exp` in the past
→ fallback text. On success it sets title, body, `threadIdentifier`, and:

| `lvl` | `interruptionLevel` | `sound` |
|---|---|---|
| `urgent` | `.timeSensitive` | `.default` |
| `active` | `.active` | `.default` |
| `passive` | `.passive` | `nil` |

Tapping re-opens the envelope with the current key and takes the destination
from the authenticated content, never from fields the relay could have set.

## 4. Send (server → relay → APNs)

```
collapse = b64u(HMAC(K_col, "collapse-v1" ‖ 0x00 ‖ scope ‖ 0x00 ‖ id))[:22]
B        = {"h":<handle>,"t":<now>,"c":<collapse, optional>,"e":<envelope>}
POST /v1/send   body B   header X-Moa-Sig: b64u(HMAC(K_send, "moa-send-v1" ‖ 0x00 ‖ SHA-256(B)))
```

`scope` and `id` come from the policy tag `scope:id` (`req`, `run`, `project`,
`event`). The signature covers the exact bytes sent.

The relay checks, cheapest first: method and path → body ≤ 4096 bytes (read
bounded, with or without `Content-Length`) → closed schema → opens `h` →
handle expiry → `now−120 ≤ t ≤ now+30` → HMAC (constant time) → rate limit per
destination (10 per 60 s) → provider JWT → APNs.

APNs request: `apns-push-type: alert`, `apns-priority: 10`, `apns-topic` from
the relay's config, `apns-expiration: now+43200`, `apns-collapse-id: c` (if
given), body:

```json
{"aps":{"alert":{"title-loc-key":"PUSH_FALLBACK_TITLE","loc-key":"PUSH_FALLBACK_BODY"},"mutable-content":1,"sound":"default"},"e":{"v":1,"k":"…","c":"…"}}
```

The relay rebuilds `e` from the validated fields; nothing else from the request
reaches APNs.

### Responses

| Status | Body | Meaning / server reaction |
|---|---|---|
| 200 | `{"ok":true}` | delivered to APNs |
| 400 | `{"error":"bad_request"}` | malformed |
| 401 | `{"error":"handle_expired"}` | the app re-registers on its next foreground; the server keeps the device |
| 401 | `{"error":"unauthorized"}` | bad handle, signature or clock |
| 404 / 405 | `{"error":"not_found"}` | |
| 410 | `{"error":"unregistered"}` | APNs 410: the server drops the registration |
| 413 | `{"error":"too_large"}` | |
| 429 | `{"error":"rate_limited"}` | |
| 502 | `{"error":"apns","status":<n>,"reason":<APNs reason>}` | any other APNs answer, e.g. 400 `BadDeviceToken` |
| 503 | `{"error":"unavailable"}` | relay misconfigured or APNs unreachable |

`/v1/register` and `/v1/confirm` use the same errors. `GET /v1/version` returns
`{"version":<commit>}`.

## Provider `fcm`

Selected by `"provider":"fcm"` in `/v1/register`. Differences from `apns`:

- **Token**: the FCM registration token, `^[A-Za-z0-9_:-]{100,512}$`.
  The cap keeps every derived artifact (challenge, handle, `/confirm` body ≤ 1024 bytes,
  handle ≤ 1024 chars on the moa server) within its limit; real tokens are ~150–200.
  `env` must be `"production"` (FCM has no sandbox); anything else is `400`.
- **Rate limits**: same budgets (3 challenges / 10 sends per 60 s per
  destination); the destination key is namespaced by provider.
- **Challenge**: a data-only message, no `notification` block:

  ```json
  {"message":{"token":"<token>","data":{"r":"<c>"},"android":{"priority":"HIGH","ttl":"300s"}}}
  ```

- **Send**: `/v1/send` is unchanged. The relay routes by the sealed `p` and
  calls `POST https://fcm.googleapis.com/v1/projects/<project_id>/messages:send`
  with `Authorization: Bearer <OAuth access token>`:

  ```json
  {"message":{"token":"<token>","data":{"v":"1","k":"<kid>","c":"<ciphertext>","col":"<collapse, if given>"},"android":{"priority":"HIGH","ttl":"43200s","collapse_key":"<collapse, if given>"}}}
  ```

  All `data` values are strings. There is **no** `notification` block: Google
  never sees a title or body; the app decrypts the envelope (`k`, `c` are
  `envelope.k`, `envelope.c`, `v` is the envelope version) and renders the
  notification itself. `col` and `collapse_key` are the request's `c`.
- **Credentials**: the Worker secret `FCM_SERVICE_ACCOUNT` holds the Google
  service-account JSON (`client_email`, `private_key` PKCS#8 PEM, `project_id`,
  optional `token_uri`, default `https://oauth2.googleapis.com/token`). The relay
  signs an RS256 JWT (`iss=client_email`,
  `scope=https://www.googleapis.com/auth/firebase.messaging`, `aud=token_uri`,
  `iat`, `exp=iat+3600`), exchanges it with
  `grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer` and keeps the access
  token in isolate memory until about 5 minutes before it expires. Nothing is
  persisted.
- **Errors** (same table as above, `502` has `"error":"fcm"`):
  FCM `404` with `errorCode: UNREGISTERED`, or `400 INVALID_ARGUMENT` with the typed `FcmError` `errorCode: INVALID_ARGUMENT`, or whose
  details name `message.token` → `410 unregistered`; FCM `5xx`, network
  failure, OAuth failure, or a missing/invalid `FCM_SERVICE_ACCOUNT` →
  `503 unavailable` (for `fcm` requests only; `apns` is unaffected, and `fcm`
  does not need any APNs secret); any other answer →
  `502 {"error":"fcm","status":<n>,"reason":<FCM status, e.g. PERMISSION_DENIED>}`.

## What a capture of each hop shows

- server → relay: the handle (opaque), time, collapse id (opaque), envelope
  (opaque, fixed size), signature, the server's IP.
- relay → APNs: token, collapse id, envelope.
- relay → FCM: token, collapse key, envelope (as strings in `data`), the
  service account's OAuth token.
- never in clear: title, body, session id, project, kind, level, destination.
