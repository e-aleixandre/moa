# moa push relay

A stateless Cloudflare Worker that forwards end-to-end encrypted notifications
from a moa server to the moa iOS app through Apple Push Notification service
(APNs). It holds the APNs key; it never holds the key that decrypts
notification content.

The wire contract is [PROTOCOL.md](PROTOCOL.md). Byte vectors shared with the Go
server and the app are in [test/vectors.json](test/vectors.json).

Endpoints: `POST /v1/register`, `POST /v1/confirm`, `POST /v1/send`,
`GET /v1/version`.

## What the relay sees and what it keeps

| Data | Seen by the relay | Kept by the relay code |
|---|---|---|
| Title, body, session, project, kind, level, destination | no (AES-GCM envelope, fixed 2048-byte plaintext) | — |
| APNs device token and environment | yes, in memory while handling `/register`, `/confirm` and `/send` | no |
| Your server's IP (`/send`) and the iPhone's IP (`/register`, `/confirm`) | yes | no |
| Time, frequency, collapse id (opaque grouping pseudonym), key id | yes | no |

- No KV, D1, R2, Durable Objects, queues, services, Tail Workers or Logpush.
  The only state is the APNs provider token cached in isolate memory and the
  Rate Limiting binding counters, keyed by a SHA-256 hash of token and
  environment. `test/config.test.js` enforces this on `wrangler.json`.
- Workers Logs and invocation logs are explicitly off, `workers.dev` and preview
  URLs are off, and `src/` contains no `console` calls (also tested).
- The handle and the signature travel in the body and a header, never in the URL.
- Errors are a closed set and never echo input, tokens or secrets.

### What Cloudflare keeps anyway

Turning logs off in the Worker does **not** mean zero retention by the
provider. From Cloudflare's own documentation (checked 2026-09-30):

- **Workers metrics** (request counts, errors, status codes, CPU/wall time) are
  aggregated automatically and queryable for up to three months; turning
  observability off is not documented to disable them.
  [Metrics and analytics](https://developers.cloudflare.com/workers/observability/metrics-and-analytics/)
- **Zone analytics** for a Worker on a custom domain: aggregated traffic for the
  last 30 days (same page).
- **Security Analytics on a custom domain, Free plan**: sampled logs of
  individual HTTP requests (including allowed ones, with request properties
  such as the client IP), 7-day retention; **Security Events**: sampled, 24 h.
  These are independent of Workers Logs.
  [Security Analytics](https://developers.cloudflare.com/waf/analytics/security-analytics/),
  [limits](https://developers.cloudflare.com/waf/analytics/security-events/#limits)
- **Workers Logs**, if they were enabled: 3 days on Free. New Workers have them
  on by default, which is why `wrangler.json` turns them off.
  [Workers Logs](https://developers.cloudflare.com/workers/observability/logs/workers-logs/),
  [pricing](https://developers.cloudflare.com/workers/platform/pricing/#workers-logs)
- **Real-time logs** (`wrangler tail`) store nothing themselves, but anyone with
  account access can watch live requests, including headers.
  [Real-time logs](https://developers.cloudflare.com/workers/observability/logs/real-time-logs/)
- **Network data**: Cloudflare's privacy policy says it collects and stores data
  derived from traffic (volumes, error rates, IP threat scores) for business
  and legal purposes, with no published maximum retention.
  [Privacy Policy §2, §11](https://www.cloudflare.com/privacypolicy/)

There is no primary evidence either way about whether Cloudflare's internal
systems keep request bodies or device tokens by default.

### Trust limits

- Open source does not prove what runs. `/v1/version` reports the commit the
  operator says was deployed; that is traceability, not attestation.
- The relay operator and Cloudflare can correlate metadata, delay, drop or
  replay envelopes, and send arbitrary APNs alerts to registered devices (they
  hold the APNs key and the sealed `K_send`). They **cannot** read or forge
  notification content: only the iPhone and its moa server know `K_enc`.
- Apple sees the device token, the encrypted envelope and timing.

## Limits

- Built for Cloudflare's free plan: best effort, no retries, no queue. If the
  relay is down, native notifications are lost (Web Push keeps working).
- Rate limits use the Workers Rate Limiting binding, which is per Cloudflare
  location, eventually consistent and permissive by design: 10 sends per
  destination per 60 s and 3 registration challenges per destination per 60 s.
  The design wanted 3 per hour, but the binding only supports 10 s or 60 s
  periods.
  [Rate Limiting binding](https://developers.cloudflare.com/workers/runtime-apis/bindings/rate-limit/)
- A captured `/send` request can be replayed for about two minutes (the `t`
  window is `now−120 … now+30`). It can only repeat a notification that was
  already sent; rejecting duplicates would need state.
- Requests that are rejected still count toward the account's daily Workers
  request quota.

## Tests

```sh
npm ci
npm test
```

Node 22+ and no dependencies beyond `wrangler` (which the tests do not use).

## Run locally

Create `.dev.vars` yourself (it is git-ignored) with throwaway values:

```sh
APNS_KEY="<PKCS#8 P-256 key, PEM or base64 DER>"
APNS_KEY_ID="ABC123DEFG"
TEAM_ID="DEF123GHIJ"
APNS_TOPIC="com.example.yourapp"
KRELAY="<32 random bytes, base64url without padding>"
```

then `npm run dev`. A valid send or register makes a real request to APNs, so
use test credentials. Locally it does not get through: `wrangler dev`'s
runtime connects to APNs over HTTP/1.1, APNs only speaks HTTP/2, and the relay
answers `503 unavailable`. The deployed Worker reaches APNs over HTTP/2.

## Deploy your own relay

A relay can only notify apps signed by the Apple Developer team that owns its
APNs key, so if you build your own app you also run your own relay.

1. In your Apple Developer account create an APNs auth key (`.p8`). Note its
   key id, your team id and the app's bundle id.
2. Set the secrets (`npx wrangler login` first):

   ```sh
   npx wrangler secret put APNS_KEY      # contents of the .p8 file
   npx wrangler secret put APNS_KEY_ID
   npx wrangler secret put TEAM_ID
   npx wrangler secret put APNS_TOPIC    # the app's bundle id
   npx wrangler secret put KRELAY        # node -e "console.log(require('crypto').randomBytes(32).toString('base64url'))"
   ```

   Changing `KRELAY` invalidates every handle: apps re-register on their next
   foreground.
3. The `namespace_id` values in `wrangler.json` must be unique within your
   Cloudflare account; change them if they collide.
4. Deploy on a custom domain (`workers.dev` is disabled on purpose):

   ```sh
   npx wrangler deploy --domain push.example.com --var VERSION:$(git rev-parse HEAD)
   ```

   or attach the domain in the dashboard (Worker → Settings → Domains & Routes).
5. Use the same URL in the app build (`MOA_PUSH_RELAY_URL`) and in your moa
   server's `push_relay_url`. The server refuses handles from any other relay
   URL.
6. After deploying, check in the dashboard that Workers Logs, Logpush and Tail
   Workers are off for the Worker.
