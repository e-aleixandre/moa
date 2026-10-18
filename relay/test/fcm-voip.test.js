import test, { beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { handle, sealBlob, openBlob, resetFCMCache } from '../src/index.js';
import { V, NOW, K_SEND, b64u, makeEnv, request, signedSend, hmac, json, enc } from './helpers.js';

// Incoming calls through FCM (Android): same sealed envelope as iOS VoIP,
// delivered data-only with a short TTL.
const TOKEN = 'fcm_' + 'aB3-_:'.repeat(30);
const KID = V.envelope.envelope.k;
const keyPair = await crypto.subtle.generateKey(
  { name: 'RSASSA-PKCS1-v1_5', modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: 'SHA-256' }, true, ['sign', 'verify']);
const pem = '-----BEGIN PRIVATE KEY-----\n'
  + btoa(String.fromCharCode(...new Uint8Array(await crypto.subtle.exportKey('pkcs8', keyPair.privateKey)))).match(/.{1,64}/g).join('\n')
  + '\n-----END PRIVATE KEY-----\n';
const SA = { client_email: 'relay@moa-test.iam.gserviceaccount.com', private_key: pem, project_id: 'moa-test', token_uri: 'https://oauth2.example.test/token' };

beforeEach(() => resetFCMCache());

const env0 = (over = {}) => makeEnv({ FCM_SERVICE_ACCOUNT: JSON.stringify(SA), ...over });
function deps0() {
  const deps = {
    calls: [],
    fcmCalls: [],
    now: () => NOW,
    jwt: async () => { throw new Error('apns must not be used'); },
    fetch: async (url, init) => {
      deps.calls.push({ url, init });
      if (url === SA.token_uri) return new Response(JSON.stringify({ access_token: 'ya29.test', expires_in: 3599, token_type: 'Bearer' }));
      deps.fcmCalls.push({ url, init });
      return new Response('{"name":"projects/moa-test/messages/1"}');
    },
  };
  return deps;
}
const body = (over = {}) => ({ token: TOKEN, env: 'production', send_key: b64u(K_SEND), provider: 'fcm', type: 'voip', ...over });
const register = (env, deps, b) => handle(request('/v1/register', { body: JSON.stringify(b) }), env, deps);
const handleFor = (env, token = TOKEN, extra = { p: 'fcm', y: 'voip' }) =>
  sealBlob(env, 'handle', { t: token, e: 'production', k: b64u(K_SEND), x: NOW + 1000, ...extra });

async function confirmVia(env, deps, token) {
  assert.equal((await register(env, deps, body({ token }))).status, 202);
  const c = JSON.parse(deps.fcmCalls.at(-1).init.body).message.data.r;
  const p = b64u(await hmac(K_SEND, 'moa-confirm-v1', enc.encode(c)));
  const confirmBody = JSON.stringify({ c, p });
  assert.ok(confirmBody.length <= 1024, `confirm body ${confirmBody.length}B`);
  const res = await handle(request('/v1/confirm', { body: confirmBody }), env, deps);
  assert.equal(res.status, 200);
  return { c, out: await json(res) };
}

test('fcm voip register: data-only challenge like alerts, sealed p and y', async () => {
  const env = env0();
  const deps = deps0();
  const { c, out } = await confirmVia(env, deps, TOKEN);
  const m = JSON.parse(deps.fcmCalls[0].init.body).message;
  assert.deepEqual(Object.keys(m.data), ['r']);
  assert.deepEqual(m.android, { priority: 'HIGH', ttl: '300s' });
  assert.ok(!('notification' in m));
  assert.deepEqual(await openBlob(env, 'challenge', c), { t: TOKEN, e: 'production', k: b64u(K_SEND), x: NOW + 300, p: 'fcm', y: 'voip' });
  assert.equal(out.provider, 'fcm');
  assert.equal(out.type, 'voip');
});

test('fcm voip send: data-only, HIGH, ttl 45s, no collapse_key', async () => {
  const env = env0();
  const deps = deps0();
  const res = await handle((await signedSend({ h: await handleFor(env) })).req(), env, deps);
  assert.equal(res.status, 200);
  assert.equal(deps.fcmCalls.length, 1);
  const { url, init } = deps.fcmCalls[0];
  assert.equal(url, 'https://fcm.googleapis.com/v1/projects/moa-test/messages:send');
  assert.equal(init.body, JSON.stringify({
    message: {
      token: TOKEN,
      data: { v: '1', k: KID, c: V.envelope.envelope.c },
      android: { priority: 'HIGH', ttl: '45s' },
    },
  }));
  assert.ok(!init.body.includes('notification'));
  assert.ok(!init.body.includes('collapse'));
});

test('fcm voip send: a collapse hint from the caller is not forwarded', async () => {
  const env = env0();
  const deps = deps0();
  const res = await handle((await signedSend({ h: await handleFor(env), c: V.collapse['req:sess_1'] })).req(), env, deps);
  assert.equal(res.status, 200);
  const m = JSON.parse(deps.fcmCalls[0].init.body).message;
  assert.deepEqual(Object.keys(m.data), ['v', 'k', 'c']);
  assert.deepEqual(m.android, { priority: 'HIGH', ttl: '45s' });
});

test('fcm voip: the real envelope with the longest token fits FCM 4096 B and every relay limit', async () => {
  const token = 'fcm_' + 'a'.repeat(508);
  const env = env0();
  const deps = deps0();
  const { out } = await confirmVia(env, deps, token);
  assert.ok(out.handle.length <= 1024, `handle ${out.handle.length} chars`);
  const sent = await signedSend({ h: out.handle });
  assert.ok(sent.bodyStr.length <= 4096, `send body ${sent.bodyStr.length}B`);
  assert.equal((await handle(sent.req(), env, deps)).status, 200);
  const m = JSON.parse(deps.fcmCalls.at(-1).init.body).message;
  const dataBytes = Object.entries(m.data).reduce((n, [k, v]) => n + enc.encode(k).length + enc.encode(v).length, 0);
  assert.ok(dataBytes <= 4096, `fcm data ${dataBytes}B`);
  assert.ok(enc.encode(JSON.stringify(m)).length <= 4096, 'whole message under the data limit too');
});

test('fcm voip: other combinations stay closed, alerts untouched', async () => {
  for (const b of [body({ env: 'sandbox' }), body({ token: 'ab'.repeat(32) }), body({ provider: 'gcm' })]) {
    const deps = deps0();
    assert.equal((await register(env0(), deps, b)).status, 400, JSON.stringify(b));
    assert.equal(deps.calls.length, 0);
  }
  const deps = deps0();
  assert.equal((await register(env0(), deps, body({ type: 'alert' }))).status, 202);
  assert.deepEqual(JSON.parse(deps.fcmCalls[0].init.body).message.android, { priority: 'HIGH', ttl: '300s' });
});

test('openBlob accepts only canonical forms of the fcm voip handle', async () => {
  const env = env0();
  const ok = await openBlob(env, 'handle', await handleFor(env));
  assert.equal(ok.p, 'fcm');
  assert.equal(ok.y, 'voip');
  // Non-canonical shapes stay refused: fcm voip with an APNs token or sandbox env,
  // (sealBlob itself never writes an unknown y).
  await assert.rejects(openBlob(env, 'handle', await handleFor(env, 'ab'.repeat(32))));
  await assert.rejects(openBlob(env, 'handle', await sealBlob(env, 'handle', { t: TOKEN, e: 'sandbox', k: b64u(K_SEND), x: NOW + 10, p: 'fcm', y: 'voip' })));
  // Pre-existing shapes still open.
  assert.equal((await openBlob(env, 'handle', await sealBlob(env, 'handle', { t: TOKEN, e: 'production', k: b64u(K_SEND), x: NOW + 10, p: 'fcm' }))).y, undefined);
});

test('fcm voip has its own rate-limit namespace', async () => {
  const env = env0();
  await register(env, deps0(), body({ type: 'alert' }));
  await register(env, deps0(), body());
  const [a, v] = env.REGISTER_LIMITER.keys;
  assert.notEqual(a, v);
  await handle((await signedSend({ h: await handleFor(env) })).req(), env, deps0());
  await handle((await signedSend({ h: await handleFor(env, TOKEN, { p: 'fcm' }) })).req(), env, deps0());
  const [s1, s2] = env.SEND_LIMITER.keys;
  assert.notEqual(s1, s2);
});
