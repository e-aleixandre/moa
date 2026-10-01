import test from 'node:test';
import assert from 'node:assert/strict';
import { handle, sealBlob, openBlob } from '../src/index.js';
import { V, NOW, hex, b64u, fromB64u, makeEnv, makeDeps, request, json, enc } from './helpers.js';

async function hkdf(ikm, info) {
  const base = await crypto.subtle.importKey('raw', ikm, 'HKDF', false, ['deriveBits']);
  return new Uint8Array(await crypto.subtle.deriveBits({ name: 'HKDF', hash: 'SHA-256', salt: new Uint8Array(), info: enc.encode(info) }, base, 256));
}

test('relay keys derive as in the vectors', async () => {
  const krelay = fromB64u(V.relay.KRELAY);
  assert.deepEqual(await hkdf(krelay, 'moa-relay-handle-v1'), hex(V.relay.K_handle));
  assert.deepEqual(await hkdf(krelay, 'moa-relay-reg-v1'), hex(V.relay.K_reg));
});

// The handle carries sealed.x (now + 90 d); the challenge expires at now + 300.
for (const [kind, sealed, x] of [['handle', V.sealed.handle, V.sealed.x], ['challenge', V.sealed.challenge, NOW + 300]]) {
  test(`sealed ${kind}: opens with KRELAY and re-seals byte-identically`, async () => {
    const env = makeEnv();
    const p = await openBlob(env, kind, sealed);
    assert.deepEqual(p, { t: V.sealed.token, e: V.sealed.env, k: b64u(hex(V.device.K_send)), x });
    assert.deepEqual(Object.keys(p), ['t', 'e', 'k', 'x']);
    const again = await sealBlob(env, kind, p, fromB64u(V.sealed.nonce));
    assert.equal(again, sealed);
  });
}

test('confirm vector: p verifies against the challenge and yields a handle', async () => {
  assert.equal(V.confirm.c, V.sealed.challenge);
  const deps = makeDeps();
  const res = await handle(request('/v1/confirm', { body: JSON.stringify(V.confirm) }), makeEnv(), deps);
  assert.equal(res.status, 200);
  const out = await json(res);
  assert.equal(out.expires_at, NOW + 90 * 86400);
  const p = await openBlob(makeEnv(), 'handle', out.handle);
  assert.deepEqual(p, { t: V.sealed.token, e: V.sealed.env, k: b64u(hex(V.device.K_send)), x: NOW + 90 * 86400 });
  assert.equal(deps.calls.length + deps.jwtCalls, 0);
});

test('send vector: exact body bytes and signature are accepted', async () => {
  const deps = makeDeps();
  const res = await handle(request('/v1/send', { body: V.send.body, headers: { 'x-moa-sig': V.send.sig } }), makeEnv(), deps);
  assert.equal(res.status, 200);
  assert.deepEqual(await json(res), { ok: true });
  assert.equal(deps.calls.length, 1);
  const sent = JSON.parse(deps.calls[0].init.body);
  assert.deepEqual(sent.e, V.envelope.envelope);
  assert.equal(deps.calls[0].init.headers['apns-collapse-id'], V.collapse['req:sess_1']);
});

test('send vector: the same JSON spelled differently fails (signature covers bytes)', async () => {
  // Signature is over bytes, not over the parsed object.
  const spaced = V.send.body.replace('{"h":', '{ "h":');
  const res = await handle(request('/v1/send', { body: spaced, headers: { 'x-moa-sig': V.send.sig } }), makeEnv(), makeDeps());
  assert.equal(res.status, 401);
  assert.deepEqual(await json(res), { error: 'unauthorized' });
});

test('envelope and collapse vectors fit the closed schema', () => {
  assert.equal(V.envelope.envelope.k.length, 11);
  assert.equal(V.envelope.envelope.c.length, 2768);
  assert.equal(fromB64u(V.envelope.envelope.c).length, 12 + 2048 + 16);
  for (const c of Object.values(V.collapse)) assert.match(c, /^[A-Za-z0-9_-]{22}$/);
  const body = JSON.parse(V.send.body);
  assert.deepEqual(Object.keys(body), ['h', 't', 'c', 'e']);
  assert.equal(body.h, V.sealed.handle);
  assert.equal(body.t, NOW);
});
