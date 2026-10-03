import { afterEach, expect, test } from 'bun:test';
import { disablePush, enablePush, getPushState, refreshPushState, watchNativeVisibleSession } from './push-client.js';

afterEach(() => { delete globalThis.window; });

function install(native) { globalThis.window = { MoaNativePush: native }; }

test('native bridge replaces Web Push for enable, disable and state', async () => {
  const calls = [];
  install({
    status: async () => ({ enabled: false, permission: 'not_determined' }),
    enable: async () => { calls.push('enable'); return { enabled: true, permission: 'granted' }; },
    disable: async () => { calls.push('disable'); return { enabled: false, permission: 'granted' }; },
  });
  await refreshPushState();
  expect(getPushState()).toBe('default');
  await enablePush();
  expect(getPushState()).toBe('subscribed');
  await disablePush();
  expect(getPushState()).toBe('default');
  expect(calls).toEqual(['enable', 'disable']);
});

test('a denied permission is reported as denied', async () => {
  install({ enable: async () => ({}), status: async () => ({ enabled: false, permission: 'denied' }) });
  await refreshPushState();
  expect(getPushState()).toBe('denied');
});

test('the visible session is reported once per change, null when hidden', () => {
  const sent = [];
  install({ enable: async () => ({}), setVisibleSession: async (id) => { sent.push(id); } });
  let state = { isMobile: true, view: 'chat', sessionsLoaded: true, activeSession: 'a', sessions: { a: {} } };
  const doc = { visibilityState: 'visible', addEventListener() {}, removeEventListener() {} };
  let notify = () => {};
  const stop = watchNativeVisibleSession({ getState: () => state, subscribe: (fn) => { notify = fn; return () => {}; }, doc });
  notify();
  expect(sent).toEqual(['a']);
  doc.visibilityState = 'hidden';
  notify();
  state = { ...state, activeSession: 'b', sessions: { b: {} } };
  doc.visibilityState = 'visible';
  notify();
  expect(sent).toEqual(['a', null, 'b']);
  stop();
});
