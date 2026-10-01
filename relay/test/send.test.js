import test from 'node:test';
import assert from 'node:assert/strict';
import { handle, sealBlob } from '../src/index.js';
import { V, NOW, K_SEND, b64u, fromB64u, randomKey, makeEnv, makeDeps, request, signedSend, json } from './helpers.js';

// Every rejection must happen before the provider JWT and the APNs fetch.
async function rejects(req, status, error, { env = makeEnv(), deps = makeDeps() } = {}) {
  const res = await handle(typeof req === 'function' ? req() : req, env, deps);
  assert.equal(res.status, status);
  assert.deepEqual(await json(res), { error });
  assert.equal(res.headers.get('cache-control'), 'no-store');
  assert.equal(deps.jwtCalls, 0, 'reached JWT');
  assert.equal(deps.calls.length, 0, 'reached fetch');
}

const DEST = { t: V.sealed.token, e: V.sealed.env, k: b64u(K_SEND) };

test('routing: unknown path 404, wrong method 405, version', async () => {
  await rejects(request('/v1/nope'), 404, 'not_found');
  await rejects(request('/v1/send/'), 404, 'not_found');
  await rejects(request('/v1/send', { method: 'GET' }), 405, 'not_found');
  await rejects(request('/v1/send', { method: 'PUT', body: V.send.body }), 405, 'not_found');
  await rejects(request('/v1/version'), 405, 'not_found');
  const res = await handle(request('/v1/version', { method: 'GET' }), makeEnv(), makeDeps());
  assert.equal(res.status, 200);
  assert.deepEqual(await json(res), { version: 'abc123' });
  const dev = await handle(request('/v1/version', { method: 'GET' }), makeEnv({ VERSION: '' }), makeDeps());
  assert.deepEqual(await json(dev), { version: 'dev' });
});

test('size: declared Content-Length over the cap is refused without reading', async () => {
  const req = new Request('https://relay.test/v1/send', {
    method: 'POST', body: new ReadableStream({ pull() { assert.fail('body was read'); } }), duplex: 'half',
    headers: { 'content-length': '4097' },
  });
  await rejects(req, 413, 'too_large');
});

test('size: 4096 bytes is fine, 4097 without Content-Length is not', async () => {
  const pad = (n) => {
    const base = JSON.parse(V.send.body);
    const s = JSON.stringify(base);
    return s.slice(0, -1) + ' '.repeat(n - s.length) + '}';
  };
  const ok = pad(4096);
  assert.equal(ok.length, 4096);
  const signed = await signedSend({ raw: ok });
  const res = await handle(request('/v1/send', { body: streamOf(ok), headers: { 'x-moa-sig': signed.sig } }), makeEnv(), makeDeps());
  assert.equal(res.status, 200);
  const big = pad(4097);
  const sig = (await signedSend({ raw: big })).sig;
  await rejects(request('/v1/send', { body: streamOf(big), headers: { 'x-moa-sig': sig } }), 413, 'too_large');
});

function streamOf(str, chunk = 100) {
  const bytes = new TextEncoder().encode(str);
  let o = 0;
  return new ReadableStream({
    pull(c) {
      if (o >= bytes.length) return c.close();
      c.enqueue(bytes.slice(o, o + chunk));
      o += chunk;
    },
  });
}

test('size: an endless body without Content-Length stops at the cap', async () => {
  let pulled = 0;
  let cancelled = false;
  const endless = new ReadableStream({
    pull(c) { pulled += 1024; c.enqueue(new Uint8Array(1024).fill(0x20)); },
    cancel() { cancelled = true; },
  });
  await rejects(request('/v1/send', { body: endless }), 413, 'too_large');
  assert.ok(pulled <= 4096 + 2048, `read ${pulled} bytes`);
  assert.ok(cancelled);
  const endless2 = new ReadableStream({ pull(c) { c.enqueue(new Uint8Array(512)); } });
  await rejects(request('/v1/register', { body: endless2 }), 413, 'too_large');
});

test('schema: malformed send bodies are 400 even when correctly signed', async () => {
  const cases = {
    'not json': { raw: '{"h":' },
    'array': { raw: '[]' },
    'null': { raw: 'null' },
    'number': { raw: '1' },
    'invalid utf-8': { raw: undefined, bytes: new Uint8Array([0x7b, 0xff, 0x7d]) },
    'extra key': { mutate: (o) => { o.x = 1; } },
    'proto key': { raw: V.send.body.slice(0, -1) + ',"__proto__":{}}' },
    'missing h': { mutate: (o) => { delete o.h; } },
    'missing t': { mutate: (o) => { delete o.t; } },
    'missing e': { mutate: (o) => { delete o.e; } },
    'h number': { mutate: (o) => { o.h = 1; } },
    't string': { mutate: (o) => { o.t = String(NOW); } },
    't float': { mutate: (o) => { o.t = NOW + 0.5; } },
    't unsafe': { mutate: (o) => { o.t = 2 ** 60; } },
    'c null': { mutate: (o) => { o.c = null; } },
    'c short': { mutate: (o) => { o.c = 'a'.repeat(21); } },
    'c long': { mutate: (o) => { o.c = 'a'.repeat(23); } },
    'c bad char': { mutate: (o) => { o.c = 'a'.repeat(21) + '+'; } },
    'e array': { mutate: (o) => { o.e = []; } },
    'e extra key': { mutate: (o) => { o.e = { ...o.e, x: 1 }; } },
    'e missing c': { mutate: (o) => { o.e = { v: 1, k: o.e.k }; } },
    'e.v 2': { mutate: (o) => { o.e = { ...o.e, v: 2 }; } },
    'e.v string': { mutate: (o) => { o.e = { ...o.e, v: '1' }; } },
    'e.k 10': { mutate: (o) => { o.e = { ...o.e, k: o.e.k.slice(1) }; } },
    'e.k 12': { mutate: (o) => { o.e = { ...o.e, k: o.e.k + 'A' }; } },
    'e.c 2767': { mutate: (o) => { o.e = { ...o.e, c: o.e.c.slice(1) }; } },
    'e.c bad char': { mutate: (o) => { o.e = { ...o.e, c: '=' + o.e.c.slice(1) }; } },
  };
  for (const [name, c] of Object.entries(cases)) {
    let req;
    if (c.bytes) {
      req = request('/v1/send', { body: c.bytes, headers: { 'x-moa-sig': V.send.sig } });
    } else {
      const s = await signedSend({ raw: c.raw, mutate: c.mutate });
      req = s.req();
    }
    await rejects(req, 400, 'bad_request').catch((e) => { e.message = `${name}: ${e.message}`; throw e; });
  }
});

test('schema: missing or malformed X-Moa-Sig is 400', async () => {
  for (const sig of [undefined, '', V.send.sig.slice(1), V.send.sig + 'A', V.send.sig.slice(0, -1) + '+', V.send.sig + '=']) {
    const headers = sig === undefined ? {} : { 'x-moa-sig': sig };
    await rejects(request('/v1/send', { body: V.send.body, headers }), 400, 'bad_request');
  }
});

test('handle: tampered, foreign, wrong kind or version is 401 unauthorized', async () => {
  const raw = fromB64u(V.sealed.handle);
  const variants = [];
  for (const i of [0, 1, 13, 40, raw.length - 1]) {
    const t = raw.slice();
    t[i] ^= 1;
    variants.push(b64u(t));
  }
  variants.push(b64u(raw.slice(0, -1)));
  variants.push(b64u(raw.slice(0, 20)));
  variants.push('');
  variants.push('!!!');
  variants.push(V.sealed.challenge);
  variants.push(await sealBlob(makeEnv({ KRELAY: randomKey() }), 'handle', { ...DEST, x: NOW + 1000 }));
  for (const h of variants) {
    const s = await signedSend({ h });
    await rejects(s.req, 401, 'unauthorized');
  }
});

test('handle: expired is 401 handle_expired, checked before the clock and the signature', async () => {
  const h = await sealBlob(makeEnv(), 'handle', { ...DEST, x: NOW });
  const s = await signedSend({ h, t: 0, kSend: new Uint8Array(32) });
  await rejects(s.req, 401, 'handle_expired');
  const fresh = await sealBlob(makeEnv(), 'handle', { ...DEST, x: NOW + 1 });
  const ok = await signedSend({ h: fresh });
  const res = await handle(ok.req(), makeEnv(), makeDeps());
  assert.equal(res.status, 200);
});

test('clock: t window is [now-120, now+30]', async () => {
  for (const [dt, status] of [[-120, 200], [-121, 401], [30, 200], [31, 401], [0, 200]]) {
    const s = await signedSend({ t: NOW + dt });
    const deps = makeDeps();
    const res = await handle(s.req(), makeEnv(), deps);
    assert.equal(res.status, status, `dt=${dt}`);
    if (status === 401) {
      assert.deepEqual(await json(res), { error: 'unauthorized' });
      assert.equal(deps.calls.length + deps.jwtCalls, 0);
    }
  }
});

test('signature: wrong key, flipped bit, or a changed body byte is 401', async () => {
  await rejects((await signedSend({ kSend: crypto.getRandomValues(new Uint8Array(32)) })).req, 401, 'unauthorized');
  const sig = fromB64u(V.send.sig);
  sig[31] ^= 0x80;
  await rejects(request('/v1/send', { body: V.send.body, headers: { 'x-moa-sig': b64u(sig) } }), 401, 'unauthorized');
  // Same schema, one envelope character changed: only the HMAC can notice.
  const i = V.send.body.indexOf('"c":"gIGC') + 10;
  const flipped = V.send.body.slice(0, i) + (V.send.body[i] === 'A' ? 'B' : 'A') + V.send.body.slice(i + 1);
  await rejects(request('/v1/send', { body: flipped, headers: { 'x-moa-sig': V.send.sig } }), 401, 'unauthorized');
  const noCollapse = V.send.body.replace(`"c":"${V.collapse['req:sess_1']}",`, '');
  await rejects(request('/v1/send', { body: noCollapse, headers: { 'x-moa-sig': V.send.sig } }), 401, 'unauthorized');
});

test('rate limit: 10 per destination per window, keyed without the token', async () => {
  const env = makeEnv();
  const deps = makeDeps();
  for (let i = 0; i < 10; i++) {
    const res = await handle((await signedSend()).req(), env, deps);
    assert.equal(res.status, 200);
  }
  const res = await handle((await signedSend()).req(), env, deps);
  assert.equal(res.status, 429);
  assert.deepEqual(await json(res), { error: 'rate_limited' });
  assert.equal(deps.calls.length, 10);
  assert.equal(deps.jwtCalls, 10);
  const keys = new Set(env.SEND_LIMITER.keys);
  assert.equal(keys.size, 1);
  for (const k of keys) {
    assert.ok(!k.includes(V.sealed.token) && !k.includes('abab'));
  }
  // Same token on the other APNs environment is a different destination.
  const prod = await sealBlob(env, 'handle', { ...DEST, e: 'production', x: NOW + 1000 });
  assert.equal((await handle((await signedSend({ h: prod })).req(), env, deps)).status, 200);
});

test('rejected requests do not consume rate limit', async () => {
  const env = makeEnv();
  for (let i = 0; i < 20; i++) {
    await rejects((await signedSend({ t: NOW - 500 })).req, 401, 'unauthorized', { env });
  }
  assert.equal(env.SEND_LIMITER.keys.length, 0);
});

test('misconfiguration is 503 and leaks nothing', async () => {
  for (const over of [{ KRELAY: undefined }, { KRELAY: 'short' }, { SEND_LIMITER: undefined }]) {
    const res = await handle((await signedSend()).req(), makeEnv(over), makeDeps());
    assert.equal(res.status, 503);
    assert.deepEqual(await json(res), { error: 'unavailable' });
  }
  const deps = makeDeps();
  const res = await handle((await signedSend()).req(), makeEnv({ APNS_TOPIC: '' }), deps);
  assert.equal(res.status, 503);
  assert.equal(deps.calls.length, 0);
});
