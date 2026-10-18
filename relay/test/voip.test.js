import test from 'node:test';
import assert from 'node:assert/strict';
import { handle, openBlob, sealBlob } from '../src/index.js';
import { V, NOW, K_SEND, b64u, makeEnv, makeDeps, request, signedSend, hmac, json, enc } from './helpers.js';

const TOKEN = 'ab'.repeat(32);

const register = (env, deps, body) => handle(request('/v1/register', { body: JSON.stringify(body) }), env, deps);
const VOIP = { token: TOKEN, env: 'sandbox', send_key: b64u(K_SEND), type: 'voip' };

test('voip register: the challenge is a VoIP push on the .voip topic, never stored', async () => {
  const env = makeEnv();
  const deps = makeDeps();
  const res = await register(env, deps, VOIP);
  assert.equal(res.status, 202);
  const { url, init } = deps.calls[0];
  assert.equal(url, `https://api.sandbox.push.apple.com/3/device/${TOKEN}`);
  assert.deepEqual(init.headers, {
    'content-type': 'application/json',
    'apns-push-type': 'voip',
    'apns-priority': '10',
    'apns-topic': 'com.example.moa.voip',
    'apns-expiration': '0',
    authorization: 'bearer test.jwt.token',
  });
  const r = JSON.parse(init.body).r;
  assert.equal(init.body, JSON.stringify({ r }));
  assert.deepEqual(await openBlob(env, 'challenge', r), { t: TOKEN, e: 'sandbox', k: b64u(K_SEND), x: NOW + 300, y: 'voip' });

  const p = b64u(await hmac(K_SEND, 'moa-confirm-v1', enc.encode(r)));
  const conf = await handle(request('/v1/confirm', { body: JSON.stringify({ c: r, p }) }), env, deps);
  assert.equal(conf.status, 200);
  const out = await json(conf);
  assert.equal(out.type, 'voip');
  assert.equal(out.token, TOKEN);

  // The handle sends VoIP: the envelope, no aps, no collapse, expiration 0.
  const sent = await handle((await signedSend({ h: out.handle })).req(), env, deps);
  assert.equal(sent.status, 200);
  const push = deps.calls[1];
  assert.equal(push.init.headers['apns-push-type'], 'voip');
  assert.equal(push.init.headers['apns-topic'], 'com.example.moa.voip');
  assert.equal(push.init.headers['apns-expiration'], '0');
  assert.ok(!('apns-collapse-id' in push.init.headers));
  assert.equal(push.init.body, JSON.stringify({ e: V.envelope.envelope }));
  assert.ok(push.init.body.length < 5120);
});

test('the alert flow is unchanged and answers type alert', async () => {
  const env = makeEnv();
  const deps = makeDeps();
  await register(env, deps, { token: TOKEN, env: 'sandbox', send_key: b64u(K_SEND) });
  assert.equal(deps.calls[0].init.headers['apns-push-type'], 'alert');
  const r = JSON.parse(deps.calls[0].init.body).r;
  assert.deepEqual(await openBlob(env, 'challenge', r), { t: TOKEN, e: 'sandbox', k: b64u(K_SEND), x: NOW + 300 });
  const p = b64u(await hmac(K_SEND, 'moa-confirm-v1', enc.encode(r)));
  const out = await json(await handle(request('/v1/confirm', { body: JSON.stringify({ c: r, p }) }), env, deps));
  assert.equal(out.type, 'alert');
});

test('the type is bound to the handle: an alert handle never sends VoIP, nor the reverse', async () => {
  const env = makeEnv();
  const alert = await sealBlob(env, 'handle', { t: TOKEN, e: 'sandbox', k: b64u(K_SEND), x: NOW + 10 });
  const voip = await sealBlob(env, 'handle', { t: TOKEN, e: 'sandbox', k: b64u(K_SEND), x: NOW + 10, y: 'voip' });
  assert.notEqual(alert, voip);
  let deps = makeDeps();
  await handle((await signedSend({ h: alert })).req(), env, deps);
  assert.equal(deps.calls[0].init.headers['apns-push-type'], 'alert');
  deps = makeDeps();
  await handle((await signedSend({ h: voip })).req(), env, deps);
  assert.equal(deps.calls[0].init.headers['apns-push-type'], 'voip');
});

test('voip register: closed schema', async () => {
  const bad = [
    { ...VOIP, type: 'VOIP' },
    { ...VOIP, type: ['voip'] },
    { ...VOIP, type: 'silent' },
    { ...VOIP, provider: 'fcm', env: 'production' }, // APNs token on FCM
  ];
  for (const body of bad) {
    const deps = makeDeps();
    const res = await register(makeEnv(), deps, body);
    assert.equal(res.status, 400, JSON.stringify(body));
    assert.equal(deps.calls.length, 0);
  }
  assert.equal((await register(makeEnv(), makeDeps(), { ...VOIP, type: 'alert' })).status, 202);
});

test('voip has its own rate-limit namespace', async () => {
  const env = makeEnv();
  await register(env, makeDeps(), { token: TOKEN, env: 'sandbox', send_key: b64u(K_SEND) });
  await register(env, makeDeps(), VOIP);
  const [a, v] = env.REGISTER_LIMITER.keys;
  assert.notEqual(a, v);
});

test('a forged fcm voip blob carrying an APNs-shaped token is refused', async () => {
  const env = makeEnv();
  const h = await sealBlob(env, 'handle', { t: TOKEN, e: 'sandbox', k: b64u(K_SEND), x: NOW + 10, y: 'voip' });
  const fcmVoip = await sealBlob(env, 'handle', { t: TOKEN, e: 'production', k: b64u(K_SEND), x: NOW + 10, p: 'fcm', y: 'voip' });
  assert.equal((await openBlob(env, 'handle', h)).y, 'voip');
  await assert.rejects(openBlob(env, 'handle', fcmVoip)); // APNs-shaped token under p:fcm
});
