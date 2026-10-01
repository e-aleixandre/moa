import test from 'node:test';
import assert from 'node:assert/strict';
import { createPublicKey, verify as opensslVerify } from 'node:crypto';
import { handle, sealBlob, openBlob, providerJWT, resetJWTCache } from '../src/index.js';
import { V, NOW, K_SEND, b64u, makeEnv, makeDeps, request, signedSend, hmac, json, enc, fromB64u, randomKey } from './helpers.js';

const dest = { t: V.sealed.token, e: V.sealed.env, k: b64u(K_SEND), x: NOW + 1000 };

async function rejectBeforeAPNs(req, env = makeEnv(), status = 401) {
  const deps = makeDeps();
  const res = await handle(req, env, deps);
  assert.equal(res.status, status);
  assert.equal(deps.calls.length, 0);
  assert.equal(deps.jwtCalls, 0);
  return res;
}

test('honest relay rejects changing every authenticated send field using the original MAC', async () => {
  const mutations = [
    o => { o.h = o.h.slice(0, -1) + (o.h.endsWith('A') ? 'B' : 'A'); },
    o => { o.t += 1; },
    o => { o.c = 'Z'.repeat(22); },
    o => { delete o.c; },
    o => { o.e.k = 'Z'.repeat(11); },
    o => { o.e.c = 'Z' + o.e.c.slice(1); },
  ];
  for (const mutate of mutations) {
    const body = JSON.parse(V.send.body);
    mutate(body);
    await rejectBeforeAPNs(request('/v1/send', { body: JSON.stringify(body), headers: { 'x-moa-sig': V.send.sig } }));
  }
});

test('different relay and different paired server key cannot use an existing handle', async () => {
  await rejectBeforeAPNs((await signedSend()).req(), makeEnv({ KRELAY: randomKey() }));
  await rejectBeforeAPNs((await signedSend({ kSend: crypto.getRandomValues(new Uint8Array(32)) })).req());
});

test('AEAD every sealed byte, truncation, version, nonce and domain alteration fail closed', async () => {
  const env = makeEnv();
  for (const kind of ['handle', 'challenge']) {
    const raw = fromB64u(await sealBlob(env, kind, dest));
    for (let i = 0; i < raw.length; i++) {
      const changed = raw.slice(); changed[i] ^= 1;
      await assert.rejects(openBlob(env, kind, b64u(changed)), err => err.status === 401);
    }
    await assert.rejects(openBlob(env, kind === 'handle' ? 'challenge' : 'handle', b64u(raw)), err => err.status === 401);
  }
});

test('token knowledge without proof never returns a handle; wrong proof cannot confirm', async () => {
  const env = makeEnv(), deps = makeDeps();
  const rogue = crypto.getRandomValues(new Uint8Array(32));
  const reg = await handle(request('/v1/register', { body: JSON.stringify({ token: dest.t, env: dest.e, send_key: b64u(rogue) }) }), env, deps);
  assert.equal(reg.status, 202);
  assert.deepEqual(await json(reg), { status: 'sent' });
  const c = JSON.parse(deps.calls[0].init.body).r;
  const p = b64u(await hmac(K_SEND, 'moa-confirm-v1', enc.encode(c)));
  const conf = await handle(request('/v1/confirm', { body: JSON.stringify({ c, p }) }), env, deps);
  assert.equal(conf.status, 401);
  assert.equal(deps.calls.length, 1);
});

test('register budget is per destination, not supplied key or minted handle', async () => {
  const env = makeEnv(), deps = makeDeps();
  for (let i = 0; i < 4; i++) {
    const res = await handle(request('/v1/register', { body: JSON.stringify({ token: dest.t, env: dest.e, send_key: randomKey() }) }), env, deps);
    assert.equal(res.status, i < 3 ? 202 : 429);
  }
  assert.equal(deps.calls.length, 3);
  assert.equal(new Set(env.REGISTER_LIMITER.keys).size, 1);
  for (let i = 0; i < 11; i++) {
    const h = await sealBlob(env, 'handle', dest);
    const res = await handle((await signedSend({ h })).req(), env, deps);
    assert.equal(res.status, i < 10 ? 200 : 429);
  }
  assert.equal(new Set(env.SEND_LIMITER.keys).size, 1);
});

test('send replay is possible only in the declared timestamp window', async () => {
  const env = makeEnv(), deps = makeDeps();
  const signed = await signedSend();
  assert.equal((await handle(signed.req(), env, deps)).status, 200);
  deps.clock = NOW + 120;
  assert.equal((await handle(signed.req(), env, deps)).status, 200);
  deps.clock = NOW + 121;
  assert.equal((await handle(signed.req(), env, deps)).status, 401);
  assert.equal(deps.calls.length, 2);
});

test('confirmation replay cannot extend authority beyond challenge expiry', async () => {
  const env = makeEnv(), deps = makeDeps();
  const c = await sealBlob(env, 'challenge', { ...dest, x: NOW + 300 });
  const p = b64u(await hmac(K_SEND, 'moa-confirm-v1', enc.encode(c)));
  const req = () => request('/v1/confirm', { body: JSON.stringify({ c, p }) });
  assert.equal((await handle(req(), env, deps)).status, 200);
  deps.clock = NOW + 299;
  assert.equal((await handle(req(), env, deps)).status, 200);
  deps.clock = NOW + 300;
  assert.equal((await handle(req(), env, deps)).status, 401);
});

test('ES256 JWT independently verifies with OpenSSL and concurrent signing coalesces', async () => {
  resetJWTCache();
  const pair = await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, true, ['sign', 'verify']);
  const der = await crypto.subtle.exportKey('pkcs8', pair.privateKey);
  const pub = await crypto.subtle.exportKey('spki', pair.publicKey);
  const env = makeEnv({ APNS_KEY: b64u(new Uint8Array(der)) });
  const promises = Array.from({ length: 30 }, () => providerJWT(env, NOW));
  assert.ok(promises.every(p => p === promises[0]));
  const tokens = await Promise.all(promises);
  const [h, p, s] = tokens[0].split('.');
  const signature = fromB64u(s);
  assert.equal(signature.length, 64);
  assert.equal(opensslVerify('sha256', Buffer.from(h + '.' + p), { key: createPublicKey({ key: Buffer.from(pub), format: 'der', type: 'spki' }), dsaEncoding: 'ieee-p1363' }, signature), true);
  signature[0] ^= 1;
  assert.equal(opensslVerify('sha256', Buffer.from(h + '.' + p), { key: createPublicKey({ key: Buffer.from(pub), format: 'der', type: 'spki' }), dsaEncoding: 'ieee-p1363' }, signature), false);
});

test('final APNs JSON fits 4096 bytes and contains no clear notification fields', async () => {
  const deps = makeDeps();
  assert.equal((await handle((await signedSend()).req(), makeEnv(), deps)).status, 200);
  const sent = deps.calls[0].init.body;
  assert.ok(Buffer.byteLength(sent) <= 4096);
  for (const marker of ['sess_1', V.envelope.plaintext?.t, V.envelope.plaintext?.b].filter(Boolean)) assert.ok(!sent.includes(marker));
});

// RED: the wire contract requires string types; RegExp.test coerces arrays.
test('RED register rejects array token before any APNs work', async () => {
  const env = makeEnv(), deps = makeDeps();
  const res = await handle(request('/v1/register', { body: JSON.stringify({ token: [dest.t], env: dest.e, send_key: b64u(K_SEND) }) }), env, deps);
  assert.equal(res.status, 400, 'array token accepted (actual status ' + res.status + ', APNs calls ' + deps.calls.length + ')');
  assert.equal(deps.calls.length, 0);
});

test('RED send rejects array kid/ciphertext/collapse before any APNs work', async () => {
  const results = [];
  for (const mutate of [o => { o.e.k = [o.e.k]; }, o => { o.e.c = [o.e.c]; }, o => { o.c = [o.c]; }]) {
    const signed = await signedSend({ mutate });
    const deps = makeDeps();
    const res = await handle(signed.req(), makeEnv(), deps);
    results.push({ status: res.status, apnsCalls: deps.calls.length });
  }
  assert.deepEqual(results, Array(3).fill({ status: 400, apnsCalls: 0 }));
});
