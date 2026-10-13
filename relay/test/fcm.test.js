import test, { beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import { handle, sealBlob, openBlob, resetFCMCache } from '../src/index.js';
import { V, NOW, K_SEND, b64u, fromB64u, makeEnv, request, signedSend, hmac, json, enc } from './helpers.js';

const TOKEN = 'fcm_' + 'aB3-_:'.repeat(30); // 4 + 180 chars
const APNS_TOKEN = 'cd'.repeat(32);
const KID = V.envelope.envelope.k;
const keyPair = await crypto.subtle.generateKey(
  { name: 'RSASSA-PKCS1-v1_5', modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: 'SHA-256' }, true, ['sign', 'verify']);
const pem = '-----BEGIN PRIVATE KEY-----\n'
  + btoa(String.fromCharCode(...new Uint8Array(await crypto.subtle.exportKey('pkcs8', keyPair.privateKey)))).match(/.{1,64}/g).join('\n')
  + '\n-----END PRIVATE KEY-----\n';
const SA = { client_email: 'relay@moa-test.iam.gserviceaccount.com', private_key: pem, project_id: 'moa-test', token_uri: 'https://oauth2.example.test/token' };

beforeEach(() => resetFCMCache());

const fcmEnv = (over = {}) => makeEnv({ FCM_SERVICE_ACCOUNT: JSON.stringify(SA), ...over });

// Routes OAuth and FCM requests; `fcm` decides the FCM answer.
function fcmDeps({ fcm = () => new Response('{"name":"projects/moa-test/messages/1"}'), oauth, now = NOW } = {}) {
  const deps = {
    calls: [],
    oauthCalls: [],
    fcmCalls: [],
    now: () => now,
    jwt: async () => { throw new Error('apns must not be used'); },
    fetch: async (url, init) => {
      deps.calls.push({ url, init });
      if (url === SA.token_uri) {
        deps.oauthCalls.push({ url, init });
        return oauth ? oauth() : new Response(JSON.stringify({ access_token: 'ya29.test', expires_in: 3599, token_type: 'Bearer' }));
      }
      deps.fcmCalls.push({ url, init });
      return fcm(url, init);
    },
  };
  return deps;
}

async function fcmHandle(env = fcmEnv(), over = {}) {
  return sealBlob(env, 'handle', { t: TOKEN, e: 'production', k: b64u(K_SEND), x: NOW + 1000, p: 'fcm', ...over });
}

const reg = (env, deps, body) => handle(request('/v1/register', { body: JSON.stringify(body) }), env, deps);
const fcmReg = (over = {}) => ({ token: TOKEN, env: 'production', send_key: b64u(K_SEND), provider: 'fcm', ...over });

test('fcm register: data-only challenge, exact request', async () => {
  const env = fcmEnv();
  const deps = fcmDeps();
  const res = await reg(env, deps, fcmReg());
  assert.equal(res.status, 202);
  assert.deepEqual(await json(res), { status: 'sent' });
  assert.equal(deps.fcmCalls.length, 1);
  const { url, init } = deps.fcmCalls[0];
  assert.equal(url, 'https://fcm.googleapis.com/v1/projects/moa-test/messages:send');
  assert.equal(init.method, 'POST');
  assert.equal(init.redirect, 'manual');
  assert.deepEqual(init.headers, { 'content-type': 'application/json', authorization: 'Bearer ya29.test' });
  const body = JSON.parse(init.body);
  assert.deepEqual(Object.keys(body), ['message']);
  assert.deepEqual(body.message.android, { priority: 'HIGH', ttl: '300s' });
  assert.equal(body.message.token, TOKEN);
  assert.deepEqual(Object.keys(body.message.data), ['r']);
  assert.ok(!('notification' in body.message));
  const blob = await openBlob(env, 'challenge', body.message.data.r);
  assert.deepEqual(blob, { t: TOKEN, e: 'production', k: b64u(K_SEND), x: NOW + 300, p: 'fcm' });
});

test('fcm register → confirm → handle that sends', async () => {
  const env = fcmEnv();
  const deps = fcmDeps();
  await reg(env, deps, fcmReg());
  const c = JSON.parse(deps.fcmCalls[0].init.body).message.data.r;
  const p = b64u(await hmac(K_SEND, 'moa-confirm-v1', enc.encode(c)));
  const res = await handle(request('/v1/confirm', { body: JSON.stringify({ c, p }) }), env, deps);
  assert.equal(res.status, 200);
  const out = await json(res);
  assert.equal(out.provider, 'fcm');
  assert.equal(out.token, TOKEN);
  assert.equal(out.env, 'production');
  assert.equal(out.expires_at, NOW + 90 * 86400);

  const sent = await handle((await signedSend({ h: out.handle })).req(), env, deps);
  assert.equal(sent.status, 200);
  assert.deepEqual(await json(sent), { ok: true });
});

test('apns confirm reports provider apns and handle has no p', async () => {
  const env = fcmEnv();
  const deps = fcmDeps();
  const res = await handle(request('/v1/register', { body: JSON.stringify({ token: APNS_TOKEN, env: 'sandbox', send_key: b64u(K_SEND) }) }), env, { ...deps, jwt: async () => 'j' });
  assert.equal(res.status, 202);
  const challenge = JSON.parse(deps.calls[0].init.body).r;
  const p = b64u(await hmac(K_SEND, 'moa-confirm-v1', enc.encode(challenge)));
  const out = await json(await handle(request('/v1/confirm', { body: JSON.stringify({ c: challenge, p }) }), env, deps));
  assert.equal(out.provider, 'apns');
  assert.deepEqual(Object.keys(await openBlob(env, 'handle', out.handle)), ['t', 'e', 'k', 'x']);
});

test('fcm send: exact URL, headers and data-only body with collapse', async () => {
  const env = fcmEnv();
  const deps = fcmDeps();
  const h = await fcmHandle(env);
  const res = await handle((await signedSend({ h })).req(), env, deps);
  assert.equal(res.status, 200);
  assert.equal(deps.fcmCalls.length, 1);
  const { url, init } = deps.fcmCalls[0];
  assert.equal(url, 'https://fcm.googleapis.com/v1/projects/moa-test/messages:send');
  assert.deepEqual(init.headers, { 'content-type': 'application/json', authorization: 'Bearer ya29.test' });
  const col = V.collapse['req:sess_1'];
  assert.equal(init.body, JSON.stringify({
    message: {
      token: TOKEN,
      data: { v: '1', k: KID, c: V.envelope.envelope.c, col },
      android: { priority: 'HIGH', ttl: '43200s', collapse_key: col },
    },
  }));
  assert.ok(!init.body.includes('notification'));
  assert.ok(Object.values(JSON.parse(init.body).message.data).every((v) => typeof v === 'string'));
});

test('fcm send: no col / collapse_key when absent', async () => {
  const env = fcmEnv();
  const deps = fcmDeps();
  const res = await handle((await signedSend({ h: await fcmHandle(env), c: null })).req(), env, deps);
  assert.equal(res.status, 200);
  const m = JSON.parse(deps.fcmCalls[0].init.body).message;
  assert.deepEqual(Object.keys(m.data), ['v', 'k', 'c']);
  assert.deepEqual(m.android, { priority: 'HIGH', ttl: '43200s' });
});

test('OAuth: JWT shape, RS256 signature verifiable, form body', async () => {
  const env = fcmEnv();
  const deps = fcmDeps();
  await handle((await signedSend({ h: await fcmHandle(env) })).req(), env, deps);
  assert.equal(deps.oauthCalls.length, 1);
  const { init } = deps.oauthCalls[0];
  assert.equal(init.method, 'POST');
  assert.equal(init.headers['content-type'], 'application/x-www-form-urlencoded');
  const form = new URLSearchParams(init.body);
  assert.equal(form.get('grant_type'), 'urn:ietf:params:oauth:grant-type:jwt-bearer');
  const [h, c, s] = form.get('assertion').split('.');
  assert.deepEqual(JSON.parse(new TextDecoder().decode(fromB64u(h))), { alg: 'RS256', typ: 'JWT' });
  assert.deepEqual(JSON.parse(new TextDecoder().decode(fromB64u(c))), {
    iss: SA.client_email, scope: 'https://www.googleapis.com/auth/firebase.messaging', aud: SA.token_uri, iat: NOW, exp: NOW + 3600,
  });
  assert.ok(await crypto.subtle.verify('RSASSA-PKCS1-v1_5', keyPair.publicKey, fromB64u(s), enc.encode(h + '.' + c)));
});

test('OAuth: default audience when token_uri is absent', async () => {
  const sa = { ...SA };
  delete sa.token_uri;
  const env = fcmEnv({ FCM_SERVICE_ACCOUNT: JSON.stringify(sa) });
  const urls = [];
  const deps = fcmDeps();
  const inner = deps.fetch;
  deps.fetch = async (url, init) => {
    urls.push(url);
    if (url === 'https://oauth2.googleapis.com/token') return new Response('{"access_token":"t","expires_in":3600}');
    return inner(url, init);
  };
  const res = await handle((await signedSend({ h: await fcmHandle(env) })).req(), env, deps);
  assert.equal(res.status, 200);
  assert.equal(urls[0], 'https://oauth2.googleapis.com/token');
});

test('OAuth: access token is cached across sends and renewed near expiry', async () => {
  const env = fcmEnv();
  const deps = fcmDeps();
  const h = await fcmHandle(env);
  for (let i = 0; i < 2; i++) assert.equal((await handle((await signedSend({ h })).req(), env, deps)).status, 200);
  assert.equal(deps.oauthCalls.length, 1);
  assert.equal(deps.fcmCalls.length, 2);

  // 3599 s lifetime, 300 s margin: at +3400 it is renewed.
  const later = fcmDeps({ now: NOW + 3400 });
  later.calls = deps.calls;
  const h2 = await fcmHandle(env, { x: NOW + 4000 });
  const res = await handle((await signedSend({ h: h2, t: NOW + 3400 })).req(), env, later);
  assert.equal(res.status, 200);
  assert.equal(later.oauthCalls.length, 1);
});

test('fcm response mapping', async () => {
  const err = (status, error) => () => new Response(JSON.stringify({ error }), { status });
  const unreg = { code: 404, status: 'NOT_FOUND', details: [{ '@type': 'type.googleapis.com/google.firebase.fcm.v1.FcmError', errorCode: 'UNREGISTERED' }] };
  const badTok = { code: 400, status: 'INVALID_ARGUMENT', details: [{ '@type': 'type.googleapis.com/google.rpc.BadRequest', fieldViolations: [{ field: 'message.token', description: 'x' }] }] };
  const cases = [
    [err(404, unreg), 410, { error: 'unregistered' }],
    [err(400, badTok), 410, { error: 'unregistered' }],
    [err(400, { code: 400, status: 'INVALID_ARGUMENT', details: [{ fieldViolations: [{ field: 'message.data' }] }] }), 502, { error: 'fcm', status: 400, reason: 'INVALID_ARGUMENT' }],
    [err(404, { code: 404, status: 'NOT_FOUND' }), 502, { error: 'fcm', status: 404, reason: 'NOT_FOUND' }],
    [err(403, { code: 403, status: 'PERMISSION_DENIED' }), 502, { error: 'fcm', status: 403, reason: 'PERMISSION_DENIED' }],
    [() => new Response('not json', { status: 429 }), 502, { error: 'fcm', status: 429, reason: 'unknown' }],
    [err(400, { status: `${TOKEN}<x>` }), 502, { error: 'fcm', status: 400, reason: 'unknown' }],
    [err(500, { code: 500, status: 'INTERNAL' }), 503, { error: 'unavailable' }],
    [err(503, { code: 503, status: 'UNAVAILABLE' }), 503, { error: 'unavailable' }],
    [() => { throw new TypeError('network down'); }, 503, { error: 'unavailable' }],
  ];
  for (const [fcm, status, body] of cases) {
    resetFCMCache();
    const env = fcmEnv();
    const res = await handle((await signedSend({ h: await fcmHandle(env) })).req(), env, fcmDeps({ fcm }));
    assert.equal(res.status, status, JSON.stringify(body));
    const text = await res.text();
    assert.deepEqual(JSON.parse(text), body);
    assert.ok(!text.includes(TOKEN));
  }
});

test('OAuth failures are 503 and are retried next time', async () => {
  const env = fcmEnv();
  const h = await fcmHandle(env);
  for (const oauth of [() => new Response('{}', { status: 401 }), () => new Response('{"nope":1}'), () => { throw new TypeError('down'); }]) {
    resetFCMCache();
    const deps = fcmDeps({ oauth });
    const res = await handle((await signedSend({ h })).req(), env, deps);
    assert.equal(res.status, 503);
    assert.deepEqual(await json(res), { error: 'unavailable' });
    assert.equal(deps.fcmCalls.length, 0);
  }
  const deps = fcmDeps({ oauth: () => new Response('{}', { status: 500 }) });
  await handle((await signedSend({ h })).req(), env, deps);
  await handle((await signedSend({ h })).req(), env, deps);
  assert.equal(deps.oauthCalls.length, 2);
});

test('missing or invalid FCM_SERVICE_ACCOUNT: 503 for fcm only, apns unaffected', async () => {
  const h = await fcmHandle(makeEnv());
  for (const bad of [undefined, '', 'not json', JSON.stringify({ ...SA, private_key: 'garbage' }), JSON.stringify({ ...SA, project_id: '../x' })]) {
    resetFCMCache();
    const env = makeEnv(bad === undefined ? {} : { FCM_SERVICE_ACCOUNT: bad });
    const deps = fcmDeps();
    let res = await handle((await signedSend({ h })).req(), env, deps);
    assert.equal(res.status, 503);
    res = await reg(env, deps, fcmReg());
    assert.equal(res.status, 503);
    assert.equal(deps.calls.length, 0);
    // The APNs vector keeps working.
    const apns = await handle((await signedSend()).req(), env, { ...deps, jwt: async () => 'j', fetch: async () => new Response('') });
    assert.equal(apns.status, 200);
  }
});

test('fcm works without any APNs secret', async () => {
  const env = fcmEnv({ APNS_TOPIC: undefined, APNS_KEY: undefined, APNS_KEY_ID: undefined, TEAM_ID: undefined });
  const res = await handle((await signedSend({ h: await fcmHandle(env) })).req(), env, fcmDeps());
  assert.equal(res.status, 200);
});

test('a handle without p still goes to APNs, even when FCM is configured', async () => {
  const env = fcmEnv();
  const deps = fcmDeps();
  deps.jwt = async () => 'test.jwt.token';
  const res = await handle((await signedSend()).req(), env, deps);
  assert.equal(res.status, 200);
  assert.equal(deps.calls[0].url, `https://api.sandbox.push.apple.com/3/device/${V.sealed.token}`);
  assert.equal(deps.oauthCalls.length, 0);
});

test('register schema: provider handling', async () => {
  const env = fcmEnv();
  const apnsBody = { token: APNS_TOKEN, env: 'sandbox', send_key: b64u(K_SEND) };
  const bad = {
    'fcm env sandbox': fcmReg({ env: 'sandbox' }),
    'fcm short token': fcmReg({ token: 'a'.repeat(99) }),
    'fcm bad chars': fcmReg({ token: 'a'.repeat(99) + '!' }),
    'fcm hex-only apns token': fcmReg({ token: APNS_TOKEN }),
    'apns with fcm token': { ...apnsBody, token: TOKEN },
    'apns explicit, fcm token': { ...apnsBody, provider: 'apns', token: TOKEN },
    'unknown provider': { ...apnsBody, provider: 'webpush' },
    'provider case': { ...apnsBody, provider: 'FCM' },
    'provider type': { ...apnsBody, provider: ['fcm'] },
    'provider null': { ...apnsBody, provider: null },
    'unknown field': fcmReg({ extra: 1 }),
    'unknown field apns': { ...apnsBody, extra: 1 },
  };
  for (const [name, body] of Object.entries(bad)) {
    const deps = fcmDeps();
    const res = await reg(env, deps, body);
    assert.equal(res.status, 400, name);
    assert.deepEqual(await json(res), { error: 'bad_request' });
    assert.equal(deps.calls.length, 0, name);
  }
  // explicit apns is the same as absent
  for (const provider of ['apns', undefined]) {
    const deps = fcmDeps();
    deps.jwt = async () => 'j';
    const res = await reg(env, deps, { ...apnsBody, provider });
    assert.equal(res.status, 202);
    assert.match(deps.calls[0].url, /^https:\/\/api\.sandbox\.push\.apple\.com\//);
  }
});


test('sealed p is authenticated and canonical', async () => {
  const env = fcmEnv();
  // fcm needs a production env and an fcm-shaped token; apns an apns-shaped one.
  for (const [blob, name] of [
    [{ t: TOKEN, e: 'sandbox', p: 'fcm' }, 'fcm sandbox'],
    [{ t: APNS_TOKEN, e: 'production', p: 'fcm' }, 'fcm with apns token'],
    [{ t: APNS_TOKEN, e: 'production', p: 'webpush' }, 'unknown p'],
  ]) {
    const h = await sealBlob(env, 'handle', { k: b64u(K_SEND), x: NOW + 100, ...blob });
    const res = await handle((await signedSend({ h })).req(), env, fcmDeps());
    assert.equal(res.status, 401, name);
  }
});

test('rate limit: fcm destination keys are namespaced; fcm 10/60 s', async () => {
  const env = fcmEnv();
  const h = await fcmHandle(env);
  const deps = fcmDeps();
  for (let i = 0; i < 10; i++) assert.equal((await handle((await signedSend({ h })).req(), env, deps)).status, 200);
  assert.equal((await handle((await signedSend({ h })).req(), env, deps)).status, 429);
  const registerKeys = fcmEnv();
  await reg(registerKeys, fcmDeps(), fcmReg());
  assert.equal(registerKeys.REGISTER_LIMITER.keys.length, 1);
  const apnsStyle = b64u(new Uint8Array(await crypto.subtle.digest('SHA-256', enc.encode('moa-rl-v1\0production\0' + TOKEN))));
  assert.notEqual(registerKeys.REGISTER_LIMITER.keys[0], apnsStyle);
});
