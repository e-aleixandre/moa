import { test, expect, afterEach } from 'bun:test';

let released;
let requests;
let sentinels;

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
        const sentinel = {
          _listeners: new Set(),
          release() { released++; this._listeners.forEach((fn) => fn()); },
          addEventListener(type, fn) { if (type === 'release') this._listeners.add(fn); },
          fireRelease() { this._listeners.forEach((fn) => fn()); },
        };
        sentinels.push(sentinel);
        return sentinel;
      },
    };
  }
  globalThis.navigator = nav;
  globalThis.document = doc;
  return doc;
}

afterEach(() => {
  delete globalThis.navigator;
  delete globalThis.document;
});

let importCounter = 0;
async function freshModule() {
  importCounter++;
  return import(`./wake-lock.js?t=${importCounter}`);
}

test('a wake-lock claim acquires and its release drops the final claim', async () => {
  installFakeEnv();
  const { claimWakeLock, __wakeLockStateForTests } = await freshModule();
  const release = claimWakeLock();
  await Promise.resolve();
  expect(__wakeLockStateForTests()).toMatchObject({ held: true, wanted: true, claims: 1 });
  release();
  expect(__wakeLockStateForTests()).toMatchObject({ held: false, wanted: false, claims: 0, listening: false });
  expect(released).toBe(1);
});

test('releasing one scope does not release another scope claim', async () => {
  installFakeEnv();
  const { claimWakeLock, __wakeLockStateForTests } = await freshModule();
  const releaseOne = claimWakeLock();
  const releaseTwo = claimWakeLock();
  await Promise.resolve();
  releaseOne();
  expect(__wakeLockStateForTests()).toMatchObject({ held: true, wanted: true, claims: 1 });
  expect(released).toBe(0);
  releaseTwo();
  expect(__wakeLockStateForTests()).toMatchObject({ held: false, wanted: false, claims: 0 });
  expect(released).toBe(1);
});

test('an active claim reacquires after WebKit drops its sentinel while hidden', async () => {
  const doc = installFakeEnv();
  const { claimWakeLock, __wakeLockStateForTests } = await freshModule();
  claimWakeLock();
  await Promise.resolve();
  sentinels[0].fireRelease();
  expect(__wakeLockStateForTests().held).toBe(false);
  doc.fireVisibility();
  await Promise.resolve();
  expect(__wakeLockStateForTests().held).toBe(true);
  expect(requests).toBe(2);
});

test('a released in-flight claim cannot adopt an orphaned sentinel', async () => {
  installFakeEnv();
  let resolveRequest;
  navigator.wakeLock.request = () => new Promise((resolve) => { resolveRequest = resolve; });
  const { claimWakeLock, __wakeLockStateForTests } = await freshModule();
  const release = claimWakeLock();
  release();
  const sentinel = { addEventListener() {}, release() { released++; } };
  resolveRequest(sentinel);
  await Promise.resolve();
  await Promise.resolve();
  expect(__wakeLockStateForTests()).toMatchObject({ held: false, wanted: false });
  expect(released).toBe(1);
});

test('hidden pages wait to acquire until visible', async () => {
  const doc = installFakeEnv({ visibility: 'hidden' });
  const { claimWakeLock, __wakeLockStateForTests } = await freshModule();
  claimWakeLock();
  await Promise.resolve();
  expect(__wakeLockStateForTests()).toMatchObject({ held: false, wanted: true, listening: true });
  doc.visibilityState = 'visible';
  doc.fireVisibility();
  await Promise.resolve();
  expect(__wakeLockStateForTests().held).toBe(true);
});

test('unsupported wake locks are safe no-ops', async () => {
  installFakeEnv({ supported: false });
  const { claimWakeLock, __wakeLockStateForTests } = await freshModule();
  expect(() => claimWakeLock()()).not.toThrow();
  expect(__wakeLockStateForTests().supported).toBe(false);
});
