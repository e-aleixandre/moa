import test from 'node:test';
import assert from 'node:assert/strict';
import { handle, providerJWT, resetJWTCache } from '../src/index.js';
import { NOW, b64u, fromB64u, makeEnv, makeDeps, signedSend, json } from './helpers.js';

async function testKey() {
  const pair = await crypto.subtle.generateKey({ name: 'ECDSA', namedCurve: 'P-256' }, true, ['sign', 'verify']);
  const der = new Uint8Array(await crypto.subtle.exportKey('pkcs8', pair.privateKey));
  const lines = Buffer.from(der).toString('base64').match(/.{1,64}/g).join('\n');
  return { pem: `-----BEGIN PRIVATE KEY-----\n${lines}\n-----END PRIVATE KEY-----\n`, pub: pair.publicKey };
}

async function verifyJWT(jwt, pub) {
  const [h, p, s] = jwt.split('.');
  const sig = fromB64u(s);
  assert.equal(sig.length, 64);
  const ok = await crypto.subtle.verify({ name: 'ECDSA', hash: 'SHA-256' }, pub, sig, new TextEncoder().encode(h + '.' + p));
  return { ok, header: JSON.parse(new TextDecoder().decode(fromB64u(h))), claims: JSON.parse(new TextDecoder().decode(fromB64u(p))) };
}

test('provider JWT: valid ES256, exact claims, cached ~40 min per isolate', async () => {
  resetJWTCache();
  const { pem, pub } = await testKey();
  const env = makeEnv({ APNS_KEY: pem });
  const first = await providerJWT(env, NOW);
  const v = await verifyJWT(first, pub);
  assert.ok(v.ok);
  assert.deepEqual(v.header, { alg: 'ES256', kid: 'KEYID12345' });
  assert.deepEqual(v.claims, { iss: 'TEAM123456', iat: NOW });
  assert.equal(await providerJWT(env, NOW + 2399), first);
  // Concurrent callers share one signing.
  const [a, b] = [providerJWT(env, NOW + 2400), providerJWT(env, NOW + 2401)];
  assert.equal(a, b);
  const second = await a;
  assert.notEqual(second, first);
  assert.equal((await verifyJWT(second, pub)).claims.iat, NOW + 2400);
  // A different key never reuses the cached token.
  const other = await testKey();
  const third = await providerJWT(makeEnv({ APNS_KEY: other.pem }), NOW + 2402);
  assert.ok((await verifyJWT(third, other.pub)).ok);
});

test('provider JWT through /v1/send: header carries a verifiable token, reused across sends', async () => {
  resetJWTCache();
  const { pem, pub } = await testKey();
  const env = makeEnv({ APNS_KEY: pem });
  const deps = makeDeps();
  delete deps.jwt;
  for (let i = 0; i < 2; i++) assert.equal((await handle((await signedSend()).req(), env, deps)).status, 200);
  const auth = deps.calls.map((c) => c.init.headers.authorization);
  assert.equal(auth[0], auth[1]);
  assert.match(auth[0], /^bearer [\w-]+\.[\w-]+\.[\w-]+$/);
  assert.ok((await verifyJWT(auth[0].slice(7), pub)).ok);
});

test('provider JWT: bad or missing key is 503 and not cached', async () => {
  resetJWTCache();
  for (const over of [{ APNS_KEY: 'not a key' }, { APNS_KEY: '' }, { APNS_KEY_ID: '' }, { TEAM_ID: undefined },
    { APNS_KEY: b64u(crypto.getRandomValues(new Uint8Array(64))) }]) {
    const deps = makeDeps();
    delete deps.jwt;
    const res = await handle((await signedSend()).req(), makeEnv(over), deps);
    assert.equal(res.status, 503);
    assert.deepEqual(await json(res), { error: 'unavailable' });
    assert.equal(deps.calls.length, 0);
  }
  const { pem } = await testKey();
  assert.match(await providerJWT(makeEnv({ APNS_KEY: pem }), NOW), /^ey/);
});
