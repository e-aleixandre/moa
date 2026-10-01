// moa native push relay. Wire contract: ../PROTOCOL.md (authoritative).
//
// The relay is stateless: everything it needs to deliver a push is sealed into
// the handle with KRELAY. It must not log, store or echo request data.

const enc = new TextEncoder();

const SEND_MAX = 4096;
const SMALL_MAX = 1024;
const RK = 0x01;
const HANDLE_TTL = 90 * 86400;
const CHALLENGE_TTL = 300;
const SEND_EXPIRATION = 43200;
const T_PAST = 120;
const T_FUTURE = 30;
// Apple: refresh no more than once per 20 min, a token is valid for at most 60.
const JWT_REFRESH = 40 * 60;
const APNS_TIMEOUT_MS = 10000;

const TOKEN_RE = /^[0-9a-f]{64,200}$/;
const COLLAPSE_RE = /^[A-Za-z0-9_-]{22}$/;
const KID_RE = /^[A-Za-z0-9_-]{11}$/;
const CIPHER_RE = /^[A-Za-z0-9_-]{2768}$/;
const ENVS = new Set(['sandbox', 'production']);
const APNS_HOSTS = { sandbox: 'api.sandbox.push.apple.com', production: 'api.push.apple.com' };

class HttpError extends Error {
  constructor(status, body) {
    super(body.error);
    this.status = status;
    this.body = body;
  }
}

const fail = (status, error) => new HttpError(status, { error });
const badRequest = () => fail(400, 'bad_request');
const unauthorized = () => fail(401, 'unauthorized');
const unavailable = () => fail(503, 'unavailable');

function reply(body, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json', 'cache-control': 'no-store' },
  });
}

// ---------- encoding ----------

export function b64u(bytes) {
  let bin = '';
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '');
}

// Strict: only the unpadded url alphabet and only the canonical encoding, so a
// value has exactly one accepted spelling.
export function fromB64u(s) {
  if (typeof s !== 'string' || !/^[A-Za-z0-9_-]*$/.test(s) || s.length % 4 === 1) return null;
  let bin;
  try {
    bin = atob(s.replaceAll('-', '+').replaceAll('_', '/'));
  } catch {
    return null;
  }
  const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
  return b64u(bytes) === s ? bytes : null;
}

function concat(...parts) {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let o = 0;
  for (const p of parts) {
    out.set(p, o);
    o += p.length;
  }
  return out;
}

// ---------- request body ----------

// Reads at most `max` bytes whatever Content-Length claims (or omits), so an
// endless body costs at most `max` bytes of memory.
async function readBounded(body, max) {
  if (!body) return new Uint8Array();
  const reader = body.getReader();
  const chunks = [];
  let size = 0;
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > max) {
      reader.cancel().catch(() => {});
      return null;
    }
    chunks.push(value);
  }
  return concat(...chunks);
}

async function readBody(request, max) {
  const declared = request.headers.get('content-length');
  if (declared !== null && !(/^\d+$/.test(declared) && Number(declared) <= max)) {
    throw fail(413, 'too_large');
  }
  const bytes = await readBounded(request.body, max);
  if (bytes === null) throw fail(413, 'too_large');
  return bytes;
}

function parseObject(bytes, required, optional = []) {
  let value;
  try {
    value = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes));
  } catch {
    throw badRequest();
  }
  if (!isObject(value)) throw badRequest();
  checkKeys(value, required, optional);
  return value;
}

function isObject(v) {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function checkKeys(obj, required, optional = []) {
  const keys = Object.keys(obj);
  for (const k of keys) if (!required.includes(k) && !optional.includes(k)) throw badRequest();
  for (const k of required) if (!Object.hasOwn(obj, k)) throw badRequest();
}

function key32(s) {
  const bytes = fromB64u(s);
  return bytes && bytes.length === 32 ? bytes : null;
}

// ---------- keys and sealed blobs ----------

async function hkdf(ikm, info) {
  const base = await crypto.subtle.importKey('raw', ikm, 'HKDF', false, ['deriveBits']);
  const bits = await crypto.subtle.deriveBits(
    { name: 'HKDF', hash: 'SHA-256', salt: new Uint8Array(), info: enc.encode(info) },
    base,
    256,
  );
  return new Uint8Array(bits);
}

const BLOBS = {
  handle: { info: 'moa-relay-handle-v1', label: 'moa-handle-v1' },
  challenge: { info: 'moa-relay-reg-v1', label: 'moa-reg-v1' },
};

async function blobKey(env, kind) {
  const krelay = key32(env.KRELAY);
  if (!krelay) throw unavailable();
  const raw = await hkdf(krelay, BLOBS[kind].info);
  return crypto.subtle.importKey('raw', raw, 'AES-GCM', false, ['encrypt', 'decrypt']);
}

function aad(kind) {
  return concat(enc.encode(BLOBS[kind].label), Uint8Array.of(RK));
}

// Exported (with an injectable nonce) so tests can reproduce the Go vectors.
export async function sealBlob(env, kind, { t, e, k, x }, nonce = crypto.getRandomValues(new Uint8Array(12))) {
  // Field order t, e, k, x is part of the byte vectors.
  const json = JSON.stringify({ t, e, k, x });
  const ct = await crypto.subtle.encrypt(
    { name: 'AES-GCM', iv: nonce, additionalData: aad(kind) },
    await blobKey(env, kind),
    enc.encode(json),
  );
  return b64u(concat(Uint8Array.of(RK), nonce, new Uint8Array(ct)));
}

// Returns the validated payload, or throws 401 for anything this relay did not
// seal with this kind (wrong key, label, version byte, or a tampered byte).
export async function openBlob(env, kind, sealed) {
  const key = await blobKey(env, kind);
  const raw = fromB64u(sealed);
  if (!raw || raw.length < 1 + 12 + 16 || raw[0] !== RK) throw unauthorized();
  let pt;
  try {
    pt = await crypto.subtle.decrypt(
      { name: 'AES-GCM', iv: raw.subarray(1, 13), additionalData: aad(kind) },
      key,
      raw.subarray(13),
    );
  } catch {
    throw unauthorized();
  }
  let p;
  try {
    p = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(pt));
  } catch {
    throw unauthorized();
  }
  if (!isObject(p) || Object.keys(p).length !== 4 || !TOKEN_RE.test(p.t) || !ENVS.has(p.e)
    || !key32(p.k) || !Number.isSafeInteger(p.x)) throw unauthorized();
  return p;
}

async function hmacVerify(keyBytes, label, data, mac) {
  const key = await crypto.subtle.importKey('raw', keyBytes, { name: 'HMAC', hash: 'SHA-256' }, false, ['verify']);
  // subtle.verify compares in constant time.
  return crypto.subtle.verify('HMAC', key, mac, concat(enc.encode(label), Uint8Array.of(0), data));
}

// Rate-limit key: the destination, hashed so the binding never sees a token.
async function destinationKey(token, env) {
  const digest = await crypto.subtle.digest('SHA-256', enc.encode('moa-rl-v1\0' + env + '\0' + token));
  return b64u(new Uint8Array(digest));
}

async function rateLimit(binding, token, apnsEnv) {
  if (!binding || typeof binding.limit !== 'function') throw unavailable();
  const { success } = await binding.limit({ key: await destinationKey(token, apnsEnv) });
  if (!success) throw fail(429, 'rate_limited');
}

// ---------- APNs ----------

let jwtCache = null;

function pkcs8(pem) {
  const der = fromB64u(String(pem).replace(/-----[^-]+-----|\s/g, '').replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, ''));
  if (!der) throw new Error('bad key');
  return der;
}

// One provider token per isolate, shared by concurrent requests: Apple rejects
// refreshing it more than once per 20 minutes.
export function providerJWT(env, now) {
  const id = env.APNS_KEY + '\0' + env.APNS_KEY_ID + '\0' + env.TEAM_ID;
  if (jwtCache && jwtCache.id === id && now - jwtCache.iat < JWT_REFRESH && now >= jwtCache.iat) {
    return jwtCache.token;
  }
  const token = (async () => {
    if (!env.APNS_KEY || !env.APNS_KEY_ID || !env.TEAM_ID) throw new Error('missing config');
    const key = await crypto.subtle.importKey('pkcs8', pkcs8(env.APNS_KEY), { name: 'ECDSA', namedCurve: 'P-256' }, false, ['sign']);
    const input = b64u(enc.encode(JSON.stringify({ alg: 'ES256', kid: env.APNS_KEY_ID })))
      + '.' + b64u(enc.encode(JSON.stringify({ iss: env.TEAM_ID, iat: now })));
    const sig = new Uint8Array(await crypto.subtle.sign({ name: 'ECDSA', hash: 'SHA-256' }, key, enc.encode(input)));
    if (sig.length !== 64) throw new Error('bad signature');
    return input + '.' + b64u(sig);
  })();
  const entry = { id, iat: now, token };
  jwtCache = entry;
  token.catch(() => {
    if (jwtCache === entry) jwtCache = null;
  });
  return token;
}

export function resetJWTCache() {
  jwtCache = null;
}

async function apnsReason(response) {
  try {
    const bytes = await readBounded(response.body, 1024);
    const reason = bytes && JSON.parse(new TextDecoder().decode(bytes)).reason;
    // Apple's reasons are plain identifiers; anything else is not echoed.
    if (typeof reason === 'string' && /^[A-Za-z0-9]{1,64}$/.test(reason)) return reason;
  } catch {
    // fall through
  }
  return 'unknown';
}

async function pushAPNs(env, deps, now, dest, payload, expiration, collapse) {
  if (!env.APNS_TOPIC) throw unavailable();
  let jwt;
  try {
    jwt = await deps.jwt(env, now);
  } catch {
    throw unavailable();
  }
  const headers = {
    'content-type': 'application/json',
    'apns-push-type': 'alert',
    'apns-priority': '10',
    'apns-topic': env.APNS_TOPIC,
    'apns-expiration': String(now + expiration),
    authorization: 'bearer ' + jwt,
  };
  if (collapse !== undefined) headers['apns-collapse-id'] = collapse;
  let response;
  try {
    response = await deps.fetch(`https://${APNS_HOSTS[dest.e]}/3/device/${dest.t}`, {
      method: 'POST',
      headers,
      body: JSON.stringify(payload),
      // The deployed runtime rejects redirect:'error'; manual + status check is equivalent.
      redirect: 'manual',
      signal: AbortSignal.timeout(APNS_TIMEOUT_MS),
    });
  } catch {
    throw unavailable();
  }
  if (response.status === 200) {
    response.body?.cancel().catch(() => {});
    return;
  }
  if (response.status === 410) {
    response.body?.cancel().catch(() => {});
    throw fail(410, 'unregistered');
  }
  throw new HttpError(502, { error: 'apns', status: response.status, reason: await apnsReason(response) });
}

// ---------- routes ----------

async function register(request, env, deps) {
  const body = parseObject(await readBody(request, SMALL_MAX), ['token', 'env', 'send_key']);
  if (!TOKEN_RE.test(body.token) || !ENVS.has(body.env) || !key32(body.send_key)) throw badRequest();
  await rateLimit(env.REGISTER_LIMITER, body.token, body.env);
  const now = deps.now();
  const c = await sealBlob(env, 'challenge', { t: body.token, e: body.env, k: body.send_key, x: now + CHALLENGE_TTL });
  const payload = { aps: { alert: { 'loc-key': 'PUSH_CHALLENGE_BODY' }, 'interruption-level': 'passive' }, r: c };
  await pushAPNs(env, deps, now, { t: body.token, e: body.env }, payload, CHALLENGE_TTL);
  return reply({ status: 'sent' }, 202);
}

async function confirm(request, env, deps) {
  const body = parseObject(await readBody(request, SMALL_MAX), ['c', 'p']);
  const p = typeof body.c === 'string' ? key32(body.p) : null;
  if (!p) throw badRequest();
  const blob = await openBlob(env, 'challenge', body.c);
  const now = deps.now();
  if (now >= blob.x) throw unauthorized();
  if (!(await hmacVerify(key32(blob.k), 'moa-confirm-v1', enc.encode(body.c), p))) throw unauthorized();
  const expiresAt = now + HANDLE_TTL;
  const handle = await sealBlob(env, 'handle', { t: blob.t, e: blob.e, k: blob.k, x: expiresAt });
  return reply({ handle, expires_at: expiresAt });
}

async function send(request, env, deps) {
  const raw = await readBody(request, SEND_MAX);
  const body = parseObject(raw, ['h', 't', 'e'], ['c']);
  const sig = key32(request.headers.get('x-moa-sig') ?? '');
  const e = body.e;
  if (typeof body.h !== 'string' || !Number.isSafeInteger(body.t)
    || (Object.hasOwn(body, 'c') && !COLLAPSE_RE.test(body.c))
    || !isObject(e) || !sig) throw badRequest();
  checkKeys(e, ['v', 'k', 'c']);
  if (e.v !== 1 || !KID_RE.test(e.k) || !CIPHER_RE.test(e.c)) throw badRequest();

  const dest = await openBlob(env, 'handle', body.h);
  const now = deps.now();
  if (now >= dest.x) throw fail(401, 'handle_expired');
  if (body.t < now - T_PAST || body.t > now + T_FUTURE) throw unauthorized();
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', raw));
  if (!(await hmacVerify(key32(dest.k), 'moa-send-v1', digest, sig))) throw unauthorized();

  await rateLimit(env.SEND_LIMITER, dest.t, dest.e);
  const payload = {
    aps: { alert: { 'title-loc-key': 'PUSH_FALLBACK_TITLE', 'loc-key': 'PUSH_FALLBACK_BODY' }, 'mutable-content': 1, sound: 'default' },
    e: { v: 1, k: e.k, c: e.c },
  };
  await pushAPNs(env, deps, now, dest, payload, SEND_EXPIRATION, body.c);
  return reply({ ok: true });
}

const ROUTES = {
  '/v1/register': ['POST', register],
  '/v1/confirm': ['POST', confirm],
  '/v1/send': ['POST', send],
  '/v1/version': ['GET', (_req, env) => reply({ version: env.VERSION || 'dev' })],
};

const defaultDeps = {
  now: () => Math.floor(Date.now() / 1000),
  fetch: (...args) => fetch(...args),
  jwt: providerJWT,
};

export async function handle(request, env, deps = {}) {
  deps = { ...defaultDeps, ...deps };
  const route = ROUTES[new URL(request.url).pathname];
  if (!route) return reply({ error: 'not_found' }, 404);
  if (request.method !== route[0]) return reply({ error: 'not_found' }, 405);
  try {
    return await route[1](request, env, deps);
  } catch (err) {
    if (err instanceof HttpError) return reply(err.body, err.status);
    return reply({ error: 'unavailable' }, 503);
  }
}

export default {
  fetch: (request, env) => handle(request, env),
};
