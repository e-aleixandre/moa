// provider-row-controller.test.js — R18 secret discipline and request rules for
// one Providers row, driven without a DOM: the row's state is a plain box the
// controller reads and writes, exactly as the component's useState does.
import { test, expect, beforeEach, afterEach } from 'bun:test';
import { createRowController, IDLE, newRowScope } from './provider-row-controller.js';
import { SAVED_COPY, TIMEOUT_COPY } from './providers-model.js';

const KEY = 'sk-ant-api03-SECRETSECRETSECRET';
const PASTE = 'abc123#state456';

function box(initial = IDLE) {
  let value = initial;
  const history = [];
  return {
    get: () => value,
    set: (next) => { value = next; history.push(next); },
    history,
  };
}

function apiError(status, error, detail) {
  const e = new Error(`${status}: ${JSON.stringify({ error, error_detail: detail })}`);
  e.status = status;
  e.userMessage = error;
  e.detail = detail;
  return e;
}

function timeout() {
  const e = new Error('Request timed out after 45000ms');
  e.name = 'TimeoutError';
  return e;
}

// fakeApi answers by path; every call is recorded.
function fakeApi(routes) {
  const calls = [];
  const fn = (method, path, body) => {
    calls.push({ method, path, body });
    for (const [suffix, answer] of Object.entries(routes)) {
      if (path.endsWith(suffix)) return typeof answer === 'function' ? answer(body) : Promise.resolve(answer);
    }
    return Promise.resolve(null);
  };
  fn.calls = calls;
  return fn;
}

const ROW = { id: 'anthropic', source: 'store', kind: 'api_key', credential_generation: 'gen-1', state: 'key_rejected', actions: ['sign_in', 'api_key'] };

function make({ api, flow = 'paste_code_state', provider = 'anthropic', openWindow, changed = [] } = {}) {
  const b = box();
  const ctl = createRowController({
    api, provider, flow, row: () => ROW, get: b.get, set: b.set,
    onChanged: (row) => changed.push(row), openWindow: openWindow || (() => null),
  });
  return { b, ctl, changed };
}

// Storage spies: any write anywhere fails the test.
let storageWrites;
const saved = {};
beforeEach(() => {
  storageWrites = [];
  for (const name of ['localStorage', 'sessionStorage']) {
    saved[name] = globalThis[name];
    globalThis[name] = {
      setItem: (k, v) => storageWrites.push([name, k, v]),
      getItem: () => null, removeItem() {}, clear() {}, key: () => null, length: 0,
    };
  }
  saved.indexedDB = globalThis.indexedDB;
  globalThis.indexedDB = { open: (...a) => { storageWrites.push(['indexedDB', ...a]); return {}; } };
  saved.log = console.log;
  saved.error = console.error;
  saved.warn = console.warn;
  const capture = (...a) => storageWrites.push(['console', ...a]);
  console.log = capture; console.error = capture; console.warn = capture;
});
afterEach(() => {
  for (const name of ['localStorage', 'sessionStorage', 'indexedDB']) {
    if (saved[name] === undefined) delete globalThis[name];
    else globalThis[name] = saved[name];
  }
  console.log = saved.log; console.error = saved.error; console.warn = saved.warn;
  expect(storageWrites).toEqual([]);
});

const noSessionCalls = (api) => expect(api.calls.filter((c) => c.path.startsWith('/api/sessions'))).toEqual([]);

// ── API key ────────────────────────────────────────────────────────────────

test('a saved key is sent once and the field is emptied before the answer', async () => {
  let seenDraftDuringRequest = null;
  let b;
  const api = fakeApi({ '/api-key': () => { seenDraftDuringRequest = b.get().draft; return Promise.resolve({ ...ROW, state: 'saved', credential_generation: 'gen-2' }); } });
  const m = make({ api });
  b = m.b;
  m.ctl.startApiKey();
  m.ctl.setDraft(KEY);
  await m.ctl.saveKey();
  expect(seenDraftDuringRequest).toBe('');
  expect(api.calls).toEqual([{ method: 'POST', path: '/api/providers/anthropic/api-key', body: { key: KEY, expected_generation: 'gen-1' } }]);
  expect(b.get()).toMatchObject({ step: 'idle', draft: '', message: { text: SAVED_COPY, tone: 'ok' } });
  expect(JSON.stringify(b.get())).not.toContain(KEY);
  noSessionCalls(api);
});

test('a rejected key empties the field too and keeps it open for the next paste', async () => {
  const api = fakeApi({ '/api-key': () => Promise.reject(apiError(400, 'That doesn\'t look like an API key. Paste the key again.', { provider: 'anthropic', class: 'invalid_key', action: 'replace_key' })) });
  const { b, ctl } = make({ api });
  ctl.startApiKey();
  ctl.setDraft(KEY);
  await ctl.saveKey();
  expect(b.get()).toMatchObject({ step: 'key', draft: '', busy: false });
  expect(b.get().message.text).toBe('That doesn\'t look like an API key. Paste the key again.');
  // The key was never in state while the request was out.
  expect(b.history.filter((h) => h.busy && h.draft)).toEqual([]);
});

test('a failed save (server error) empties the field and ends the attempt', async () => {
  const api = fakeApi({ '/api-key': () => Promise.reject(apiError(503, 'Could not save credentials. Start again.', { provider: 'anthropic', class: 'persistence_failed', action: 'start_again' })) });
  const { b, ctl } = make({ api });
  ctl.startApiKey();
  ctl.setDraft(KEY);
  await ctl.saveKey();
  expect(b.get()).toMatchObject({ step: 'idle', draft: '' });
  expect(b.get().message.text).toBe('Could not save credentials. Start again.');
});

test('a key save that times out is not sent again; the status is re-read', async () => {
  const changed = [];
  const api = fakeApi({ '/api-key': () => Promise.reject(timeout()) });
  const { b, ctl } = make({ api, changed });
  ctl.startApiKey();
  ctl.setDraft(KEY);
  await ctl.saveKey();
  expect(api.calls.filter((c) => c.path.endsWith('/api-key'))).toHaveLength(1);
  expect(b.get()).toMatchObject({ step: 'idle', draft: '', message: { text: TIMEOUT_COPY } });
  expect(changed).toEqual([null]); // null = re-read the list
});

test('cancel empties the field and sends nothing', () => {
  const api = fakeApi({});
  const { b, ctl } = make({ api });
  ctl.startApiKey();
  ctl.setDraft(KEY);
  ctl.cancel();
  expect(b.get()).toEqual(IDLE);
  expect(api.calls).toEqual([]);
});

test('dispose (unmount) empties the field', () => {
  const api = fakeApi({});
  const { b, ctl } = make({ api });
  ctl.startApiKey();
  ctl.setDraft(KEY);
  ctl.dispose();
  expect(b.get().draft).toBe('');
});

test('reopening the key form starts empty', () => {
  const { b, ctl } = make({ api: fakeApi({}) });
  ctl.startApiKey();
  ctl.setDraft(KEY);
  ctl.cancel();
  ctl.startApiKey();
  expect(b.get().draft).toBe('');
});

// ── Paste sign-in ──────────────────────────────────────────────────────────

const BEGIN = { provider: 'anthropic', flow: 'paste_code_state', attempt_id: 'att-1', authorize_url: 'https://claude.ai/oauth/authorize?x=1', expires_at: '2026-10-03T21:00:00Z' };

test('sign-in opens the window in the click, cuts the opener, then navigates', async () => {
  const order = [];
  const handle = {
    closed: false,
    set opener(v) { order.push(['opener', v]); },
    location: { set href(v) { order.push(['href', v]); } },
  };
  let openedSync = false;
  const api = fakeApi({ '/oauth/begin': () => Promise.resolve(BEGIN) });
  const { b, ctl } = make({ api, openWindow: () => { openedSync = true; return handle; } });
  ctl.startSignIn();
  const pending = ctl.open();
  expect(openedSync).toBe(true); // before any await
  await pending;
  expect(order).toEqual([['opener', null], ['href', BEGIN.authorize_url]]);
  expect(b.get()).toMatchObject({ step: 'paste', opened: true });
  expect(api.calls[0]).toEqual({ method: 'POST', path: '/api/providers/anthropic/oauth/begin', body: { expected_generation: 'gen-1' } });
});

test('a blocked popup leaves the page with a link to offer instead', async () => {
  const api = fakeApi({ '/oauth/begin': () => Promise.resolve(BEGIN) });
  const { b, ctl } = make({ api, openWindow: () => null });
  ctl.startSignIn();
  await ctl.open();
  expect(b.get()).toMatchObject({ step: 'paste', opened: false });
  expect(b.get().attempt.authorize_url).toBe(BEGIN.authorize_url);
});

test('completing sends the paste once, clears it, and sends no turn', async () => {
  let draftDuring = null;
  let b;
  const api = fakeApi({
    '/oauth/begin': () => Promise.resolve(BEGIN),
    '/oauth/complete': () => { draftDuring = b.get().draft; return Promise.resolve({ provider: { ...ROW, state: 'saved', kind: 'oauth' }, next_action: 'return_to_session' }); },
  });
  const m = make({ api, changed: [] });
  b = m.b;
  m.ctl.startSignIn();
  await m.ctl.open();
  m.ctl.setDraft(`  ${PASTE}  `);
  await m.ctl.complete();
  expect(draftDuring).toBe('');
  expect(api.calls.map((c) => c.path)).toEqual(['/api/providers/anthropic/oauth/begin', '/api/providers/anthropic/oauth/complete']);
  expect(api.calls[1].body).toEqual({ attempt_id: 'att-1', input: PASTE });
  expect(b.get()).toMatchObject({ step: 'idle', draft: '', message: { text: SAVED_COPY } });
  expect(m.changed[0]).toMatchObject({ id: 'anthropic', state: 'saved' });
  noSessionCalls(api);
});

test('a complete that times out is never repeated', async () => {
  const api = fakeApi({ '/oauth/begin': () => Promise.resolve(BEGIN), '/oauth/complete': () => Promise.reject(timeout()) });
  const { b, ctl } = make({ api });
  ctl.startSignIn();
  await ctl.open();
  ctl.setDraft(PASTE);
  await ctl.complete();
  await new Promise((r) => setTimeout(r, 10));
  expect(api.calls.filter((c) => c.path.endsWith('/oauth/complete'))).toHaveLength(1);
  expect(b.get()).toMatchObject({ step: 'idle', draft: '', message: { text: TIMEOUT_COPY } });
});

test('a paste the server cannot read keeps the attempt and empties the field', async () => {
  const api = fakeApi({
    '/oauth/begin': () => Promise.resolve(BEGIN),
    '/oauth/complete': () => Promise.reject(apiError(400, 'That isn\'t what the sign-in page showed. Paste it again.', { provider: 'anthropic', class: 'invalid_input', action: 'paste_again' })),
  });
  const { b, ctl } = make({ api });
  ctl.startSignIn();
  await ctl.open();
  ctl.setDraft(PASTE);
  await ctl.complete();
  expect(b.get()).toMatchObject({ step: 'paste', draft: '', busy: false });
  expect(b.get().attempt.attempt_id).toBe('att-1');
});

test('cancel ends the attempt on the server and empties the paste', async () => {
  const api = fakeApi({ '/oauth/begin': () => Promise.resolve(BEGIN), '/oauth/cancel': () => Promise.resolve(null) });
  const { b, ctl } = make({ api });
  ctl.startSignIn();
  await ctl.open();
  ctl.setDraft(PASTE);
  ctl.cancel();
  expect(b.get()).toEqual(IDLE);
  expect(api.calls.at(-1)).toEqual({ method: 'POST', path: '/api/providers/anthropic/oauth/cancel', body: { attempt_id: 'att-1' } });
});

test('a failed begin closes the blank window it opened', async () => {
  let closed = false;
  const handle = { closed: false, close() { closed = true; }, location: {} };
  const api = fakeApi({ '/oauth/begin': () => Promise.reject(apiError(409, 'Credentials changed. Reload and try again.', { provider: 'anthropic', class: 'credentials_changed', action: 'reload' })) });
  const changed = [];
  const { b, ctl } = make({ api, openWindow: () => handle, changed });
  ctl.startSignIn();
  await ctl.open();
  expect(closed).toBe(true);
  expect(b.get().step).toBe('idle');
  expect(changed).toEqual([null]);
});

// ── Device (xAI) ───────────────────────────────────────────────────────────

const DEVICE = { provider: 'xai', flow: 'device', attempt_id: 'dev-1', user_code: 'WXYZ-1234', verification_uri: 'https://accounts.x.ai/device', expires_at: '2026-10-03T21:00:00Z', state: 'waiting' };

test('device sign-in opens no window and reports server progress', async () => {
  let opened = false;
  const answers = [{ state: 'waiting' }, { state: 'exchanging' }, { state: 'saved', provider_status: { id: 'xai', state: 'saved' } }];
  const api = fakeApi({ '/oauth/begin': () => Promise.resolve(DEVICE), '/oauth/progress': () => Promise.resolve(answers.shift()) });
  const changed = [];
  const { b, ctl } = make({ api, flow: 'device', provider: 'xai', openWindow: () => { opened = true; return null; }, changed });
  ctl.startSignIn();
  await ctl.open();
  expect(opened).toBe(false);
  expect(b.get()).toMatchObject({ step: 'device', progress: 'waiting' });
  await ctl.poll();
  await ctl.poll();
  expect(b.get().progress).toBe('exchanging');
  await ctl.poll();
  expect(b.get()).toMatchObject({ step: 'idle', message: { text: SAVED_COPY } });
  expect(changed[0]).toMatchObject({ id: 'xai' });
  // Ended: further ticks ask nothing.
  const before = api.calls.length;
  await ctl.poll();
  expect(api.calls.length).toBe(before);
  noSessionCalls(api);
});

test('a denied device sign-in ends with its own words', async () => {
  const api = fakeApi({ '/oauth/begin': () => Promise.resolve(DEVICE), '/oauth/progress': () => Promise.resolve({ state: 'denied' }) });
  const { b, ctl } = make({ api, flow: 'device', provider: 'xai' });
  ctl.startSignIn();
  await ctl.open();
  await ctl.poll();
  expect(b.get()).toMatchObject({ step: 'idle', message: { text: 'Sign-in was not approved. Start again.' } });
});

test('a network blip while polling is not an answer', async () => {
  const api = fakeApi({ '/oauth/begin': () => Promise.resolve(DEVICE), '/oauth/progress': () => Promise.reject(new TypeError('Failed to fetch')) });
  const { b, ctl } = make({ api, flow: 'device', provider: 'xai' });
  ctl.startSignIn();
  await ctl.open();
  await ctl.poll();
  expect(b.get().step).toBe('device');
});

// ── Retry saving ───────────────────────────────────────────────────────────

test('retry saving sends no credential', async () => {
  const api = fakeApi({ '/retry-save': () => Promise.resolve({ ...ROW, state: 'saved' }) });
  const { b, ctl } = make({ api });
  await ctl.retrySave();
  expect(api.calls).toEqual([{ method: 'POST', path: '/api/providers/anthropic/retry-save', body: {} }]);
  expect(b.get().message.tone).toBe('ok');
});

// F5 — Cancel / unmount while Begin is pending.
for (const how of ['cancel', 'dispose']) {
  test(`late begin does not resurrect sign-in after ${how}`, async () => {
    const b = box();
    let resolve;
    const requests = [];
    let navigated = '';
    const handle = { closed: false, opener: {}, location: { set href(v) { navigated = v; } }, close() { this.closed = true; } };
    const scope = newRowScope();
    const api = (method, path, body) => {
      requests.push({ method, path, body });
      return path.endsWith('/begin') ? new Promise((r) => { resolve = r; }) : Promise.resolve(null);
    };
    // A fresh controller per call, as the component rebuilds it per render.
    const mk = () => createRowController({
      api, provider: 'anthropic', flow: 'paste_code_state', row: () => ROW, get: b.get, set: b.set,
      openWindow: () => handle, scope,
    });
    mk().startSignIn();
    const inFlight = mk().open();
    mk()[how]();
    resolve({ provider: 'anthropic', flow: 'paste_code_state', attempt_id: 'late-attempt', authorize_url: 'https://claude.ai/oauth/authorize?state=x' });
    await inFlight;
    expect(navigated).toBe('');
    expect(b.get().step).not.toBe('paste');
    expect(handle.closed).toBe(true);
    const cancels = requests.filter((r) => r.path.endsWith('/cancel'));
    if (how === 'cancel') {
      expect(cancels.length).toBe(1);
      expect(cancels[0].body.attempt_id).toBe('late-attempt');
    } else {
      expect(cancels.length).toBe(0);
    }
  });
}

test('late begin failure after cancel stays quiet', async () => {
  const b = box();
  let reject;
  const api = (m, path) => (path.endsWith('/begin') ? new Promise((_, r) => { reject = r; }) : Promise.resolve(null));
  const ctl = createRowController({ api, provider: 'anthropic', flow: 'paste_code_state', row: () => ROW, get: b.get, set: b.set, openWindow: () => null });
  ctl.startSignIn();
  const p = ctl.open();
  ctl.cancel();
  reject(apiError(500, 'boom'));
  await p;
  expect(b.get().message).toBeNull();
  expect(b.get().step).toBe('idle');
});

test('late key save does not overwrite a newer operation', async () => {
  const b = box();
  let resolve;
  const api = (m, path) => (path.endsWith('/api-key') ? new Promise((r) => { resolve = r; }) : Promise.resolve(null));
  const ctl = createRowController({ api, provider: 'anthropic', flow: 'paste_code_state', row: () => ROW, get: b.get, set: b.set, openWindow: () => null });
  ctl.startApiKey();
  ctl.setDraft(KEY);
  const p = ctl.saveKey();
  ctl.cancel();
  ctl.startSignIn();
  resolve({ id: 'anthropic' });
  await p;
  expect(b.get().step).toBe('signin');
});
