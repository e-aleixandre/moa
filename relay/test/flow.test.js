import test from 'node:test';
import assert from 'node:assert/strict';
import { handle, openBlob, sealBlob } from '../src/index.js';
import { V, NOW, K_SEND, b64u, fromB64u, makeEnv, makeDeps, request, signedSend, hmac, json, enc } from './helpers.js';

const TOKEN = 'cd'.repeat(32);

test('send → APNs: exact URL, headers and rebuilt payload', async () => {
  const deps = makeDeps();
  const res = await handle((await signedSend()).req(), makeEnv(), deps);
  assert.equal(res.status, 200);
  assert.equal(deps.calls.length, 1);
  const { url, init } = deps.calls[0];
  assert.equal(url, `https://api.sandbox.push.apple.com/3/device/${V.sealed.token}`);
  assert.equal(init.method, 'POST');
  assert.equal(init.redirect, 'manual');
  assert.ok(init.signal instanceof AbortSignal);
  assert.deepEqual(init.headers, {
    'content-type': 'application/json',
    'apns-push-type': 'alert',
    'apns-priority': '10',
    'apns-topic': 'com.example.moa',
    'apns-expiration': String(NOW + 43200),
    'apns-collapse-id': V.collapse['req:sess_1'],
    authorization: 'bearer test.jwt.token',
  });
  assert.equal(init.body, JSON.stringify({
    aps: { alert: { 'title-loc-key': 'PUSH_FALLBACK_TITLE', 'loc-key': 'PUSH_FALLBACK_BODY' }, 'mutable-content': 1, sound: 'default' },
    e: V.envelope.envelope,
  }));
});

test('send → APNs: no collapse id when absent, production host from the handle', async () => {
  const env = makeEnv({ APNS_TOPIC: 'org.other.app' });
  const h = await sealBlob(env, 'handle', { t: TOKEN, e: 'production', k: b64u(K_SEND), x: NOW + 10 });
  const deps = makeDeps();
  const res = await handle((await signedSend({ h, c: null })).req(), env, deps);
  assert.equal(res.status, 200);
  const { url, init } = deps.calls[0];
  assert.equal(url, `https://api.push.apple.com/3/device/${TOKEN}`);
  assert.equal(init.headers['apns-topic'], 'org.other.app');
  assert.ok(!('apns-collapse-id' in init.headers));
});

test('APNs response mapping', async () => {
  const cases = [
    [{ status: 410, body: '{"reason":"Unregistered","timestamp":1}' }, 410, { error: 'unregistered' }],
    [{ status: 400, body: '{"reason":"BadDeviceToken"}' }, 502, { error: 'apns', status: 400, reason: 'BadDeviceToken' }],
    [{ status: 403, body: '{"reason":"InvalidProviderToken"}' }, 502, { error: 'apns', status: 403, reason: 'InvalidProviderToken' }],
    [{ status: 500, body: 'not json' }, 502, { error: 'apns', status: 500, reason: 'unknown' }],
    [{ status: 400, body: `{"reason":"${V.sealed.token}<x>"}` }, 502, { error: 'apns', status: 400, reason: 'unknown' }],
    [{ status: 400, body: `{"reason":"${'A'.repeat(65)}"}` }, 502, { error: 'apns', status: 400, reason: 'unknown' }],
    [{ status: 302, body: '' }, 502, { error: 'apns', status: 302, reason: 'unknown' }],
    [{ throws: true }, 503, { error: 'unavailable' }],
    [{ jwtThrows: true }, 503, { error: 'unavailable' }],
  ];
  for (const [opts, status, body] of cases) {
    const deps = makeDeps(opts);
    const res = await handle((await signedSend()).req(), makeEnv(), deps);
    assert.equal(res.status, status, JSON.stringify(opts));
    const text = await res.text();
    assert.deepEqual(JSON.parse(text), body);
    assert.ok(!text.includes(V.sealed.token));
    if (opts.jwtThrows) assert.equal(deps.calls.length, 0);
  }
});

async function register(env, deps, body = { token: TOKEN, env: 'sandbox', send_key: b64u(K_SEND) }) {
  return handle(request('/v1/register', { body: JSON.stringify(body) }), env, deps);
}

test('register → challenge push → confirm → handle that can send', async () => {
  const env = makeEnv();
  const deps = makeDeps();
  const res = await register(env, deps);
  assert.equal(res.status, 202);
  assert.deepEqual(await json(res), { status: 'sent' });
  assert.equal(deps.calls.length, 1);
  const { url, init } = deps.calls[0];
  assert.equal(url, `https://api.sandbox.push.apple.com/3/device/${TOKEN}`);
  assert.deepEqual(init.headers, {
    'content-type': 'application/json',
    'apns-push-type': 'alert',
    'apns-priority': '10',
    'apns-topic': 'com.example.moa',
    'apns-expiration': String(NOW + 300),
    authorization: 'bearer test.jwt.token',
  });
  const payload = JSON.parse(init.body);
  const r = payload.r;
  assert.equal(init.body, JSON.stringify({ aps: { alert: { 'loc-key': 'PUSH_CHALLENGE_BODY' }, 'interruption-level': 'passive' }, r }));
  assert.deepEqual(await openBlob(env, 'challenge', r), { t: TOKEN, e: 'sandbox', k: b64u(K_SEND), x: NOW + 300 });

  const p = b64u(await hmac(K_SEND, 'moa-confirm-v1', enc.encode(r)));
  const conf = await handle(request('/v1/confirm', { body: JSON.stringify({ c: r, p }) }), env, deps);
  assert.equal(conf.status, 200);
  const { handle: h, expires_at, token, env: destEnv } = await json(conf);
  assert.equal(expires_at, NOW + 90 * 86400);
  // The app binds the confirmation to the registration it waits on with these.
  assert.equal(token, TOKEN);
  assert.equal(destEnv, 'sandbox');
  assert.equal(deps.calls.length, 1, 'confirm must not push');

  const sent = await handle((await signedSend({ h })).req(), env, deps);
  assert.equal(sent.status, 200);
  assert.equal(deps.calls[1].url, `https://api.sandbox.push.apple.com/3/device/${TOKEN}`);
});

test('confirm rejects a wrong key, an expired or tampered challenge, and a handle', async () => {
  const env = makeEnv();
  const deps = makeDeps();
  await register(env, deps);
  const r = JSON.parse(deps.calls[0].init.body).r;
  const good = b64u(await hmac(K_SEND, 'moa-confirm-v1', enc.encode(r)));
  const confirm = (c, p, clock = NOW) => {
    deps.clock = clock;
    return handle(request('/v1/confirm', { body: JSON.stringify({ c, p }) }), env, deps);
  };
  const expect401 = async (res) => {
    assert.equal(res.status, 401);
    assert.deepEqual(await json(res), { error: 'unauthorized' });
  };
  await expect401(await confirm(r, b64u(await hmac(crypto.getRandomValues(new Uint8Array(32)), 'moa-confirm-v1', enc.encode(r)))));
  await expect401(await confirm(r, b64u(await hmac(K_SEND, 'moa-send-v1', enc.encode(r)))));
  await expect401(await confirm(r, good, NOW + 300));
  assert.equal((await confirm(r, good, NOW + 299)).status, 200);
  const raw = fromB64u(r);
  raw[raw.length - 1] ^= 1;
  const tampered = b64u(raw);
  await expect401(await confirm(tampered, b64u(await hmac(K_SEND, 'moa-confirm-v1', enc.encode(tampered)))));
  const h = V.sealed.handle;
  await expect401(await confirm(h, b64u(await hmac(K_SEND, 'moa-confirm-v1', enc.encode(h)))));
  assert.equal(deps.calls.length, 1);
  assert.equal(deps.jwtCalls, 1);
});

test('register and confirm: closed schemas', async () => {
  const good = { token: TOKEN, env: 'sandbox', send_key: b64u(K_SEND) };
  const sk = b64u(K_SEND);
  const bad = [
    { ...good, extra: 1 },
    { token: TOKEN, env: 'sandbox' },
    { ...good, token: TOKEN.toUpperCase() },
    { ...good, token: 'a'.repeat(63) },
    { ...good, token: 'a'.repeat(201) },
    { ...good, token: 123 },
    { ...good, env: 'development' },
    { ...good, send_key: b64u(K_SEND.slice(1)) },
    { ...good, send_key: sk + '=' },
    // Same bytes in a non-canonical spelling (low bits of the last char set).
    { ...good, send_key: sk.slice(0, -1) + String.fromCharCode(sk.charCodeAt(42) + 1) },
    { ...good, send_key: sk.slice(0, -1) + '+' },
  ];
  assert.equal(sk.at(-1), 'M');
  for (const body of bad) {
    const deps = makeDeps();
    const res = await handle(request('/v1/register', { body: JSON.stringify(body) }), makeEnv(), deps);
    assert.equal(res.status, 400, JSON.stringify(body));
    assert.equal(deps.calls.length + deps.jwtCalls, 0);
  }
  for (const body of [{ c: V.confirm.c }, { ...V.confirm, x: 1 }, { c: 1, p: V.confirm.p }, { c: V.confirm.c, p: V.confirm.p.slice(1) }, []]) {
    const res = await handle(request('/v1/confirm', { body: JSON.stringify(body) }), makeEnv(), makeDeps());
    assert.equal(res.status, 400, JSON.stringify(body));
  }
  const big = await handle(request('/v1/confirm', { body: JSON.stringify({ c: 'A'.repeat(1100), p: V.confirm.p }) }), makeEnv(), makeDeps());
  assert.equal(big.status, 413);
});

test('register: 3 challenges per destination per window, before any push', async () => {
  const env = makeEnv();
  const deps = makeDeps();
  for (let i = 0; i < 3; i++) assert.equal((await register(env, deps)).status, 202);
  const res = await register(env, deps);
  assert.equal(res.status, 429);
  assert.deepEqual(await json(res), { error: 'rate_limited' });
  assert.equal(deps.calls.length, 3);
  assert.ok(env.REGISTER_LIMITER.keys.every((k) => !k.includes(TOKEN)));
});

test('register: APNs 410 and errors map like send', async () => {
  const gone = await register(makeEnv(), makeDeps({ status: 410 }));
  assert.equal(gone.status, 410);
  assert.deepEqual(await json(gone), { error: 'unregistered' });
  const bad = await register(makeEnv(), makeDeps({ status: 400, body: '{"reason":"DeviceTokenNotForTopic"}' }));
  assert.deepEqual(await json(bad), { error: 'apns', status: 400, reason: 'DeviceTokenNotForTopic' });
});
