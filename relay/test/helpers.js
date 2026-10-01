import { readFileSync } from 'node:fs';
import { b64u, fromB64u } from '../src/index.js';

export const V = JSON.parse(readFileSync(new URL('./vectors.json', import.meta.url), 'utf8'));
export const NOW = V.envelope.now;
export const enc = new TextEncoder();
export const hex = (s) => Uint8Array.from(s.match(/../g), (h) => parseInt(h, 16));
export const K_SEND = hex(V.device.K_send);
export { b64u, fromB64u };

export function randomKey() {
  return b64u(crypto.getRandomValues(new Uint8Array(32)));
}

// Mirrors the binding semantics closely enough: a fixed budget per key.
export function limiter(limit) {
  const counts = new Map();
  return {
    keys: [],
    async limit({ key }) {
      this.keys.push(key);
      const n = (counts.get(key) ?? 0) + 1;
      counts.set(key, n);
      return { success: n <= limit };
    },
  };
}

export function makeEnv(over = {}) {
  return {
    KRELAY: V.relay.KRELAY,
    APNS_TOPIC: 'com.example.moa',
    APNS_KEY: 'unused-because-jwt-is-mocked',
    APNS_KEY_ID: 'KEYID12345',
    TEAM_ID: 'TEAM123456',
    VERSION: 'abc123',
    SEND_LIMITER: limiter(10),
    REGISTER_LIMITER: limiter(3),
    ...over,
  };
}

// Counts every expensive step so tests can assert a rejection happened before it.
export function makeDeps({ now = NOW, status = 200, body = '', throws = false, jwtThrows = false } = {}) {
  const deps = {
    calls: [],
    jwtCalls: 0,
    clock: now,
    now: () => deps.clock,
    jwt: async () => {
      deps.jwtCalls++;
      if (jwtThrows) throw new Error('key import failed');
      return 'test.jwt.token';
    },
    fetch: async (url, init) => {
      deps.calls.push({ url, init });
      if (throws) throw new TypeError('network down');
      return new Response(body, { status });
    },
  };
  return deps;
}

export function request(path, { method = 'POST', body, headers = {} } = {}) {
  const init = { method, headers };
  if (body !== undefined) {
    init.body = body;
    if (body instanceof ReadableStream) init.duplex = 'half';
  }
  return new Request('https://relay.test' + path, init);
}

export async function hmac(key, label, data) {
  const k = await crypto.subtle.importKey('raw', key, { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  const msg = new Uint8Array([...enc.encode(label), 0, ...data]);
  return new Uint8Array(await crypto.subtle.sign('HMAC', k, msg));
}

export async function sendSig(kSend, bodyStr) {
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', enc.encode(bodyStr)));
  return b64u(await hmac(kSend, 'moa-send-v1', digest));
}

// A signed /v1/send request; `mutate` edits the body object before it is
// serialized and signed, so schema cases still carry a valid signature.
export async function signedSend({ h = V.sealed.handle, t = NOW, c = V.collapse['req:sess_1'], e = V.envelope.envelope, kSend = K_SEND, mutate, raw } = {}) {
  let bodyStr = raw;
  if (bodyStr === undefined) {
    const obj = { h, t, ...(c === null ? {} : { c }), e }; // c: null omits it
    if (mutate) mutate(obj);
    bodyStr = JSON.stringify(obj);
  }
  const sig = await sendSig(kSend, bodyStr);
  return { bodyStr, sig, req: () => request('/v1/send', { body: bodyStr, headers: { 'x-moa-sig': sig } }) };
}

export async function json(res) {
  return JSON.parse(await res.text());
}
