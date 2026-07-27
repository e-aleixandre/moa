// wake-lock.test.js — run with `bun test`.
//
// bun's default test environment has no DOM and no navigator.wakeLock, so these
// tests install a minimal fake navigator/document on globalThis for the
// duration of each test. Because wake-lock.js reads `supported` once at import
// time, the fakes are installed here before the dynamic import inside each test
// (a fresh module instance per test via the query-string cache-buster).

import { test, expect, afterEach } from 'bun:test';

let released; // count of sentinel.release() calls in the current test
let requests; // count of navigator.wakeLock.request() calls
let sentinels; // live fake sentinels, so a test can fire their 'release' event

function installFakeEnv({ supported = true, visibility = 'visible' } = {}) {
  released = 0;
  requests = 0;
  sentinels = [];
  const doc = {
    visibilityState: visibility,
    _listeners: new Set(),
    addEventListener(type, fn) { if (type === 'visibilitychange') this._listeners.add(fn); },
    removeEventListener(type, fn) { if (type === 'visibilitychange') this._listeners.delete(fn); },
    fireVisibility() { this._listeners.forEach((fn) => fn()); },
  };
  const nav = {};
  if (supported) {
    nav.wakeLock = {
      async request() {
        requests++;
        const s = {
          _rel: new Set(),
          released: false,
          release() { this.released = true; released++; this._rel.forEach((fn) => fn()); },
          addEventListener(type, fn) { if (type === 'release') this._rel.add(fn); },
          fireRelease() { this._rel.forEach((fn) => fn()); },
        };
        sentinels.push(s);
        return s;
      },
    };
  }
  globalThis.navigator = nav;
  globalThis.document = doc;
  return doc;
}

function uninstall() {
  delete globalThis.navigator;
  delete globalThis.document;
}

afterEach(uninstall);

// Fresh module instance per test so module-level `supported`/`sentinel`/`wanted`
// reflect the env installed just before.
let importCounter = 0;
async function freshModule() {
  importCounter++;
  return import(`./wake-lock.js?t=${importCounter}`);
}

test('requestWakeLock acquires a screen lock when supported', async () => {
  installFakeEnv();
  const { requestWakeLock, __wakeLockStateForTests } = await freshModule();
  requestWakeLock();
  // request() is async; let the microtask settle.
  await Promise.resolve();
  const st = __wakeLockStateForTests();
  expect(st.supported).toBe(true);
  expect(st.wanted).toBe(true);
  expect(st.held).toBe(true);
  expect(requests).toBe(1);
});

test('releaseWakeLock releases the lock and stops wanting it', async () => {
  installFakeEnv();
  const { requestWakeLock, releaseWakeLock, __wakeLockStateForTests } = await freshModule();
  requestWakeLock();
  await Promise.resolve();
  releaseWakeLock();
  const st = __wakeLockStateForTests();
  expect(st.wanted).toBe(false);
  expect(st.held).toBe(false);
  expect(st.listening).toBe(false);
  expect(released).toBe(1);
});

test('returning to foreground re-acquires the lock if still wanted', async () => {
  const doc = installFakeEnv({ visibility: 'visible' });
  const { requestWakeLock, __wakeLockStateForTests } = await freshModule();
  requestWakeLock();
  await Promise.resolve();
  // Simulate the OS dropping the lock (screen locked / backgrounded).
  sentinels[0].fireRelease();
  expect(__wakeLockStateForTests().held).toBe(false);
  // Back to foreground while still wanted → re-acquire.
  doc.fireVisibility();
  await Promise.resolve();
  expect(__wakeLockStateForTests().held).toBe(true);
  expect(requests).toBe(2);
});

test('a visibilitychange after release does not re-acquire', async () => {
  const doc = installFakeEnv();
  const { requestWakeLock, releaseWakeLock, __wakeLockStateForTests } = await freshModule();
  requestWakeLock();
  await Promise.resolve();
  releaseWakeLock();
  doc.fireVisibility();
  await Promise.resolve();
  expect(__wakeLockStateForTests().held).toBe(false);
  expect(requests).toBe(1);
});

test('a release during an in-flight request does not leave an orphaned lock', async () => {
  // Make request() resolve on our command so we can release mid-flight.
  installFakeEnv();
  let resolveReq;
  navigator.wakeLock.request = () => new Promise((res) => {
    resolveReq = () => {
      const s = {
        _rel: new Set(),
        released: false,
        release() { this.released = true; released++; this._rel.forEach((fn) => fn()); },
        addEventListener(type, fn) { if (type === 'release') this._rel.add(fn); },
      };
      sentinels.push(s);
      res(s);
    };
  });
  const { requestWakeLock, releaseWakeLock, __wakeLockStateForTests } = await freshModule();
  requestWakeLock();
  // Release before the request resolves.
  releaseWakeLock();
  // Now let the pending request resolve — its sentinel must be released, not held.
  resolveReq();
  await Promise.resolve();
  await Promise.resolve();
  const st = __wakeLockStateForTests();
  expect(st.held).toBe(false);
  expect(st.wanted).toBe(false);
  // The stale sentinel was released.
  expect(released).toBe(1);
});

test('is a safe no-op when the Wake Lock API is unsupported', async () => {
  installFakeEnv({ supported: false });
  const { requestWakeLock, releaseWakeLock, __wakeLockStateForTests } = await freshModule();
  expect(() => { requestWakeLock(); releaseWakeLock(); }).not.toThrow();
  const st = __wakeLockStateForTests();
  expect(st.supported).toBe(false);
  expect(st.held).toBe(false);
});

test('does not acquire while the page is hidden, but arms the listener', async () => {
  const doc = installFakeEnv({ visibility: 'hidden' });
  const { requestWakeLock, __wakeLockStateForTests } = await freshModule();
  requestWakeLock();
  await Promise.resolve();
  // Hidden → no lock yet, but wanted + listening so it acquires on foreground.
  expect(__wakeLockStateForTests().held).toBe(false);
  expect(__wakeLockStateForTests().listening).toBe(true);
  doc.visibilityState = 'visible';
  doc.fireVisibility();
  await Promise.resolve();
  expect(__wakeLockStateForTests().held).toBe(true);
});
