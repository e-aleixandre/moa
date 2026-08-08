// AttentionReceipt.test.js — hook-level receipt lifecycle tests run with `bun test`.
import { afterEach, beforeEach, expect, mock, test } from "bun:test";

let currentHook = 0;
let hooks = [];
let needsRender = false;

function changed(previous, next) {
  return !previous || previous.length !== next.length || previous.some((value, index) => !Object.is(value, next[index]));
}

mock.module("preact/hooks", () => ({
  useState(initial) {
    const index = currentHook++;
    if (!(index in hooks)) hooks[index] = { value: typeof initial === "function" ? initial() : initial };
    return [hooks[index].value, (value) => {
      hooks[index].value = typeof value === "function" ? value(hooks[index].value) : value;
      needsRender = true;
    }];
  },
  useRef(initial) {
    const index = currentHook++;
    if (!(index in hooks)) hooks[index] = { current: initial };
    return hooks[index];
  },
  useCallback(callback, deps) {
    const index = currentHook++;
    const hook = hooks[index] || (hooks[index] = {});
    if (changed(hook.deps, deps)) {
      hook.deps = deps;
      hook.callback = callback;
    }
    return hook.callback;
  },
  useEffect(callback, deps) {
    const index = currentHook++;
    const hook = hooks[index] || (hooks[index] = {});
    if (changed(hook.deps, deps)) {
      hook.deps = deps;
      hook.callback = callback;
      hook.dirty = true;
    }
  },
}));

const {
  PROMPT_RECEIPT_MIN_VISIBLE_PX,
  isMeaningfullyIntersecting,
  isMeaningfullyInViewport,
  ResolvedAttentionReceipt,
  usePendingPromptAttentionReceipt,
} = await import("./AttentionReceipt.jsx");
const { StaleServerInstanceError } = await import("../../data/api.js");
const { store, setState, updateSession } = await import("../../data/store.js");

function createHarness(props) {
  let current = props;
  const ref = { current: props.element };
  const render = () => {
    currentHook = 0;
    needsRender = false;
    usePendingPromptAttentionReceipt(ref, current.sessionId, current.pending, current.acknowledge, current.refreshInstances);
    for (const hook of hooks) {
      if (!hook?.dirty) continue;
      hook.dirty = false;
      hook.cleanup?.();
      hook.cleanup = hook.callback() || null;
    }
  };
  return {
    render,
    rerender(next) { current = { ...current, ...next }; render(); },
    settle() { while (needsRender) render(); },
    unmount() {
      for (const hook of hooks) hook?.cleanup?.();
    },
  };
}

function createResolvedHarness(props) {
  let current = props;
  const render = () => {
    currentHook = 0;
    needsRender = false;
    ResolvedAttentionReceipt(current);
    if (current.element) hooks[0].current = current.element;
    for (const hook of hooks) {
      if (!hook?.dirty) continue;
      hook.dirty = false;
      hook.cleanup?.();
      hook.cleanup = hook.callback() || null;
    }
  };
  return {
    render,
    rerender(next) { current = { ...current, ...next }; render(); },
    settle() { while (needsRender) render(); },
    unmount() {
      for (const hook of hooks) hook?.cleanup?.();
    },
  };
}

function fakeDocument(hidden = false) {
  const listeners = new Map();
  return {
    hidden,
    documentElement: { clientHeight: 400, clientWidth: 400 },
    addEventListener(type, listener) {
      if (!listeners.has(type)) listeners.set(type, new Set());
      listeners.get(type).add(listener);
    },
    removeEventListener(type, listener) { listeners.get(type)?.delete(listener); },
    emit(type) { for (const listener of listeners.get(type) || []) listener(); },
    listenerCount(type) { return listeners.get(type)?.size || 0; },
  };
}

function fakeWindow() {
  const listeners = new Map();
  return {
    innerHeight: 400,
    innerWidth: 400,
    addEventListener(type, listener) {
      if (!listeners.has(type)) listeners.set(type, new Set());
      listeners.get(type).add(listener);
    },
    removeEventListener(type, listener) { listeners.get(type)?.delete(listener); },
    emit(type) { for (const listener of listeners.get(type) || []) listener(); },
    listenerCount(type) { return listeners.get(type)?.size || 0; },
  };
}

const originalDocument = globalThis.document;
const originalWindow = globalThis.window;
const originalObserver = globalThis.IntersectionObserver;
const originalFetch = globalThis.fetch;
const originalStorage = globalThis.localStorage;
let storageValues;

beforeEach(() => {
  hooks = [];
  needsRender = false;
  globalThis.document = fakeDocument(false);
  globalThis.window = fakeWindow();
  globalThis.fetch = () => Promise.resolve(new Response(""));
  storageValues = new Map();
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: {
      get length() { return storageValues.size; },
      key: (index) => [...storageValues.keys()][index] || null,
      getItem: (key) => storageValues.get(key) || null,
      setItem: (key, value) => storageValues.set(key, value),
      removeItem: (key) => storageValues.delete(key),
    },
  });
  setState({
    isMobile: true, activeSession: "s1", drawerOpen: false, paletteOpen: false,
    conversationObscuringOverlayCount: 0,
    sessions: { s1: { id: "s1", unseen: true, unseenGen: 7, serverInstance: "a", subagents: {} } },
  });
});

afterEach(() => {
  if (originalDocument === undefined) delete globalThis.document;
  else globalThis.document = originalDocument;
  if (originalWindow === undefined) delete globalThis.window;
  else globalThis.window = originalWindow;
  if (originalObserver === undefined) delete globalThis.IntersectionObserver;
  else globalThis.IntersectionObserver = originalObserver;
  globalThis.fetch = originalFetch;
  if (originalStorage === undefined) delete globalThis.localStorage;
  else Object.defineProperty(globalThis, "localStorage", { configurable: true, value: originalStorage });
});

async function flush(harness) {
  for (let i = 0; i < 4; i++) {
    await Promise.resolve();
    harness.settle();
  }
}

function onScreenElement(height = 100) {
  return { getBoundingClientRect: () => ({ top: 50, bottom: 50 + height, left: 50, right: 150, width: 100, height }) };
}

function elementInScrollport(scrollportClass, elementRect, scrollportRect) {
  const scrollport = { getBoundingClientRect: () => scrollportRect };
  return {
    getBoundingClientRect: () => elementRect,
    closest: (selector) => selector.includes(scrollportClass) ? scrollport : null,
  };
}

function confirmedReceipt(sessionId, pending) {
  const session = store.get().sessions[sessionId];
  updateSession(sessionId, {
    unseen: false,
    lastAckedUnseenGen: pending.unseenGen,
    lastAckedUnseenInstance: pending.serverInstance,
  });
  return Promise.resolve(true);
}

test("requires a fixed visible region and disconnects only after a confirmed receipt", async () => {
  const observers = [];
  globalThis.IntersectionObserver = class {
    constructor(callback, options) { this.callback = callback; this.options = options; this.disconnects = 0; observers.push(this); }
    observe(element) { this.element = element; }
    disconnect() { this.disconnects++; }
  };
  const harness = createHarness({ sessionId: "s1", pending: { id: "ask", unseenGen: 7, serverInstance: "a" }, element: { getBoundingClientRect: () => ({ top: 500, bottom: 600, left: 50, right: 150, width: 100, height: 100 }) } });
  harness.render();
  expect(observers).toHaveLength(1);
  expect(observers[0].options.threshold).toBe(0);
  observers[0].callback([{ isIntersecting: true, boundingClientRect: { width: 100, height: 100 }, intersectionRect: { width: 100, height: 1 } }]);
  harness.settle();
  expect(observers[0].disconnects).toBe(0);
  observers[0].callback([{ isIntersecting: true, boundingClientRect: { width: 100, height: 100 }, intersectionRect: { width: 100, height: PROMPT_RECEIPT_MIN_VISIBLE_PX } }]);
  await flush(harness);
  expect(store.get().sessions.s1.unseen).toBe(false);
  expect(storageValues.size).toBe(0);
  harness.unmount();
  expect(observers[0].disconnects).toBeGreaterThan(0);
});

test("a prompt seen while hidden retries and converges when foregrounded", async () => {
  const observers = [];
  globalThis.document.hidden = true;
  globalThis.IntersectionObserver = class {
    constructor(callback) { this.callback = callback; this.disconnects = 0; observers.push(this); }
    observe() {}
    disconnect() { this.disconnects++; }
  };
  const harness = createHarness({ sessionId: "s1", pending: { id: "ask", unseenGen: 7, serverInstance: "a" }, element: onScreenElement() });
  harness.render();
  observers[0].callback([{ isIntersecting: true, boundingClientRect: { width: 100, height: 100 }, intersectionRect: { width: 100, height: 100 } }]);
  harness.settle();
  expect(store.get().sessions.s1.unseen).toBe(true);
  expect(observers[0].disconnects).toBe(0);
  globalThis.document.hidden = false;
  globalThis.document.emit("visibilitychange");
  await flush(harness);
  expect(store.get().sessions.s1.unseen).toBe(false);
  harness.unmount();
  expect(observers[0].disconnects).toBeGreaterThan(0);
});

test("a hidden observer callback cannot launder an off-screen prompt on foreground", async () => {
  const observers = [];
  globalThis.document.hidden = true;
  globalThis.IntersectionObserver = class {
    constructor(callback) { this.callback = callback; observers.push(this); }
    observe() {}
    disconnect() {}
  };
  const harness = createHarness({
    sessionId: "s1", pending: { id: "ask", unseenGen: 7, serverInstance: "a" },
    element: { getBoundingClientRect: () => ({ top: 500, bottom: 600, left: 50, right: 150, width: 100, height: 100 }) },
  });
  harness.render();
  observers[0].callback([{ isIntersecting: true, boundingClientRect: { width: 100, height: 100 }, intersectionRect: { width: 100, height: 100 } }]);
  harness.settle();
  globalThis.document.hidden = false;
  globalThis.document.emit("visibilitychange");
  await flush(harness);
  expect(store.get().sessions.s1.unseen).toBe(true);
  harness.unmount();
});

test("an observer that never fires still acknowledges through the geometric safety net", async () => {
  globalThis.IntersectionObserver = class { observe() {} disconnect() {} };
  const element = onScreenElement();
  expect(isMeaningfullyInViewport(element)).toBe(true);
  const harness = createHarness({ sessionId: "s1", pending: { id: "ask", unseenGen: 7, serverInstance: "a" }, element });
  harness.render();
  await flush(harness);
  expect(store.get().sessions.s1.unseen).toBe(false);
  harness.unmount();
  expect(globalThis.window.listenerCount("scroll")).toBe(0);
  expect(globalThis.window.listenerCount("resize")).toBe(0);
});

test("a visibly mounted prompt still acknowledges when durable receipt storage is blocked", async () => {
  delete globalThis.IntersectionObserver;
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: {
      get length() { return 0; }, key() { return null; }, getItem() { return null; },
      setItem() { throw new Error("quota exhausted"); }, removeItem() {},
    },
  });
  // The foreground geometry is the in-memory proof for this mount. Storage is
  // only needed to carry that proof through a later reload.
  const harness = createHarness({
    sessionId: "s1", pending: { id: "ask", unseenGen: 7, serverInstance: "a" }, element: onScreenElement(),
  });
  harness.render();
  await flush(harness);
  expect(store.get().sessions.s1.unseen).toBe(false);
  harness.unmount();
});

for (const scrollportClass of ["stream-scroll", "mconv-stream"]) {
  test(`${scrollportClass} does not acknowledge a prompt below its scrollport even when it is in the window`, async () => {
    delete globalThis.IntersectionObserver;
    const element = elementInScrollport(
      scrollportClass,
      { top: 200, bottom: 300, left: 50, right: 150, width: 100, height: 100 },
      { top: 0, bottom: 100, left: 0, right: 400, width: 400, height: 100 },
    );
    expect(isMeaningfullyInViewport(element)).toBe(false);
    const harness = createHarness({
      sessionId: "s1", pending: { id: "ask", unseenGen: 7, serverInstance: "a" }, element,
    });
    harness.render();
    await flush(harness);
    expect(store.get().sessions.s1.unseen).toBe(true);
    harness.unmount();
  });

  test(`${scrollportClass} acknowledges a prompt meaningfully visible inside its scrollport`, async () => {
    delete globalThis.IntersectionObserver;
    const element = elementInScrollport(
      scrollportClass,
      { top: 50, bottom: 150, left: 50, right: 150, width: 100, height: 100 },
      { top: 0, bottom: 400, left: 0, right: 400, width: 400, height: 400 },
    );
    expect(isMeaningfullyInViewport(element)).toBe(true);
    const harness = createHarness({
      sessionId: "s1", pending: { id: "ask", unseenGen: 7, serverInstance: "a" }, element,
    });
    harness.render();
    await flush(harness);
    expect(store.get().sessions.s1.unseen).toBe(false);
    harness.unmount();
  });
}

test("a prompt ten viewports tall has a reachable visible-region receipt", async () => {
  delete globalThis.IntersectionObserver;
  const element = onScreenElement(4000);
  const harness = createHarness({ sessionId: "s1", pending: { id: "tall", unseenGen: 7, serverInstance: "a" }, element });
  harness.render();
  await flush(harness);
  expect(store.get().sessions.s1.unseen).toBe(false);
  harness.unmount();
});

test("a failed read remains observed and retries after the server later accepts it", async () => {
  delete globalThis.IntersectionObserver;
  setState({
    activeSession: "retry-fence",
    sessions: { "retry-fence": { id: "retry-fence", unseen: true, unseenGen: 7, serverInstance: "retry-fence", subagents: {} } },
  });
  let calls = 0;
  let accepted = false;
  const acknowledge = (sessionId, pending) => {
    calls++;
    return accepted ? confirmedReceipt(sessionId, pending) : Promise.reject(new Error("server rejected fence"));
  };
  const harness = createHarness({ sessionId: "retry-fence", pending: { id: "ask", unseenGen: 7, serverInstance: "retry-fence" }, element: onScreenElement(), acknowledge });
  harness.render();
  await flush(harness);
  expect(store.get().sessions["retry-fence"].unseen).toBe(true);
  accepted = true;
  globalThis.window.emit("online");
  await flush(harness);
  expect(calls).toBe(2);
  expect(store.get().sessions["retry-fence"].unseen).toBe(false);
  harness.unmount();
});

test("offline reads retry when the browser reports online", async () => {
  delete globalThis.IntersectionObserver;
  setState({
    activeSession: "retry-offline",
    sessions: { "retry-offline": { id: "retry-offline", unseen: true, unseenGen: 7, serverInstance: "retry-offline", subagents: {} } },
  });
  let online = false;
  let calls = 0;
  const acknowledge = (sessionId, pending) => {
    calls++;
    return online ? confirmedReceipt(sessionId, pending) : Promise.reject(new TypeError("offline"));
  };
  const harness = createHarness({ sessionId: "retry-offline", pending: { id: "ask", unseenGen: 7, serverInstance: "retry-offline" }, element: onScreenElement(), acknowledge });
  harness.render();
  await flush(harness);
  expect(store.get().sessions["retry-offline"].unseen).toBe(true);
  online = true;
  globalThis.window.emit("online");
  await flush(harness);
  expect(calls).toBe(2);
  expect(store.get().sessions["retry-offline"].unseen).toBe(false);
  harness.unmount();
});

test("a stale read fence refreshes the roster before retrying the replacement instance", async () => {
  delete globalThis.IntersectionObserver;
  setState({
    activeSession: "stale-fence",
    sessions: {
      "stale-fence": {
        id: "stale-fence", title: "Stale", state: "idle", cwd: "/x", messages: [], subagents: {},
        unseen: true, unseenGen: 7, serverInstance: "instance-a",
        pendingAsk: { id: "ask", unseenGen: 7, serverInstance: "instance-a" },
      },
    },
  });
  const instances = [];
  let refreshes = 0;
  const acknowledge = (_, pending) => {
    instances.push(pending.serverInstance);
    return pending.serverInstance === "instance-a"
      ? Promise.reject(new StaleServerInstanceError("stale-fence", 7, "instance-a"))
      : confirmedReceipt("stale-fence", pending);
  };
  const harness = createHarness({
    sessionId: "stale-fence",
    pending: { id: "ask", unseenGen: 7, serverInstance: "instance-a" },
    element: onScreenElement(),
    acknowledge,
    refreshInstances: () => {
      refreshes++;
      updateSession("stale-fence", { serverInstance: "instance-b" });
      return Promise.resolve();
    },
  });
  try {
    harness.render();
    for (let i = 0; i < 10 && refreshes === 0; i++) {
      await new Promise((resolve) => setTimeout(resolve, 0));
      harness.settle();
    }
    expect(refreshes).toBe(1);
    expect(store.get().sessions["stale-fence"].serverInstance).toBe("instance-b");

    harness.rerender({ pending: { id: "ask", unseenGen: 7, serverInstance: "instance-b" } });
    await flush(harness);
    expect(instances).toEqual(["instance-a", "instance-b"]);
    expect(store.get().sessions["stale-fence"].unseen).toBe(false);
  } finally {
    harness.unmount();
  }
});

test("a retained receipt retries immediately when the app returns to foreground", async () => {
  let accepted = false;
  let calls = 0;
  const session = {
    id: "s1", unseen: true, unseenGen: 7, serverInstance: "a", subagents: {},
    resolvedPendingAttention: { id: "ask", unseenGen: 7, serverInstance: "a" },
  };
  setState({ sessions: { s1: session } });
  const acknowledge = () => {
    calls++;
    return accepted
      ? Promise.resolve(true)
      : Promise.reject(new TypeError("offline"));
  };
  const harness = createResolvedHarness({ session, acknowledge, element: onScreenElement() });
  harness.render();
  await flush(harness);
  expect(calls).toBe(1);
  accepted = true;
  globalThis.document.hidden = false;
  globalThis.document.emit("visibilitychange");
  await flush(harness);
  await new Promise((resolve) => setTimeout(resolve, 0));
  harness.settle();
  expect(calls).toBe(2);
  expect(store.get().sessions.s1.resolvedPendingAttention).toBeNull();
  harness.unmount();
});

test("a remote-resolution receipt stays unread until its visible notice is inside the scrollport", async () => {
  delete globalThis.IntersectionObserver;
  const session = {
    id: "s1", unseen: true, unseenGen: 7, serverInstance: "a", subagents: {},
    resolvedPendingAttention: { id: "ask", unseenGen: 7, serverInstance: "a" },
  };
  setState({ sessions: { s1: session } });
  let calls = 0;
  const hiddenReceipt = elementInScrollport(
    "mconv-stream",
    { top: 200, bottom: 300, left: 50, right: 150, width: 100, height: 100 },
    { top: 0, bottom: 100, left: 0, right: 400, width: 400, height: 100 },
  );
  const harness = createResolvedHarness({
    session, acknowledge: () => { calls++; return Promise.resolve(true); }, element: hiddenReceipt,
  });
  harness.render();
  await flush(harness);
  expect(calls).toBe(0);
  expect(store.get().sessions.s1.resolvedPendingAttention).not.toBeNull();
  harness.unmount();
});

test("a stale resolved-receipt fence refreshes once before acknowledging the replacement instance", async () => {
  delete globalThis.IntersectionObserver;
  const initial = {
    id: "stale-resolved", unseen: true, unseenGen: 7, serverInstance: "instance-a", subagents: {},
    resolvedPendingAttention: { id: "ask", unseenGen: 7, serverInstance: "instance-a" },
  };
  setState({ activeSession: "stale-resolved", sessions: { "stale-resolved": initial } });
  const instances = [];
  let refreshes = 0;
  const acknowledge = (_, pending) => {
    instances.push(pending.serverInstance);
    return pending.serverInstance === "instance-a"
      ? Promise.reject(new StaleServerInstanceError("stale-resolved", 7, "instance-a"))
      : confirmedReceipt("stale-resolved", pending);
  };
  const harness = createResolvedHarness({
    session: initial, acknowledge, element: onScreenElement(),
    refreshInstances: () => {
      refreshes++;
      updateSession("stale-resolved", { serverInstance: "instance-b" });
      return Promise.resolve();
    },
  });
  try {
    harness.render();
    for (let i = 0; i < 10 && refreshes === 0; i++) {
      await new Promise((resolve) => setTimeout(resolve, 0));
      harness.settle();
    }
    expect(refreshes).toBe(1);
    const replacement = {
      ...store.get().sessions["stale-resolved"],
      resolvedPendingAttention: { id: "ask", unseenGen: 7, serverInstance: "instance-b" },
    };
    updateSession("stale-resolved", replacement);
    harness.rerender({ session: replacement });
    await flush(harness);
    expect(instances).toEqual(["instance-a", "instance-b"]);
    expect(store.get().sessions["stale-resolved"].resolvedPendingAttention).toBeNull();
  } finally {
    harness.unmount();
  }
});

test("switching sessions replaces the observer and unmounting leaves none behind", () => {
  const observers = [];
  globalThis.IntersectionObserver = class {
    constructor(callback) { this.callback = callback; this.disconnects = 0; observers.push(this); }
    observe() {}
    disconnect() { this.disconnects++; }
  };
  const harness = createHarness({ sessionId: "s1", pending: { id: "ask", unseenGen: 7, serverInstance: "a" }, element: {} });
  harness.render();
  setState({
    activeSession: "s2",
    sessions: { s2: { id: "s2", unseen: true, unseenGen: 7, serverInstance: "a", subagents: {} } },
  });
  harness.rerender({ sessionId: "s2" });
  expect(observers).toHaveLength(2);
  expect(observers[0].disconnects).toBeGreaterThan(0);
  harness.unmount();
  expect(observers[1].disconnects).toBeGreaterThan(0);
});

test("the threshold rejects a one-pixel sliver", () => {
  expect(isMeaningfullyIntersecting({ isIntersecting: true, boundingClientRect: { width: 100, height: 100 }, intersectionRect: { width: 100, height: 1 } })).toBe(false);
  expect(isMeaningfullyIntersecting({ isIntersecting: true, boundingClientRect: { width: 100, height: 100 }, intersectionRect: { width: 100, height: PROMPT_RECEIPT_MIN_VISIBLE_PX } })).toBe(true);
});

test("zero-area and display:none prompts are never a visible receipt", () => {
  expect(isMeaningfullyIntersecting({
    isIntersecting: true,
    boundingClientRect: { width: 0, height: 0 },
    intersectionRect: { width: 0, height: 0 },
  })).toBe(false);
  expect(isMeaningfullyInViewport({
    getBoundingClientRect: () => ({ top: 50, bottom: 50, left: 50, right: 50, width: 0, height: 0 }),
  })).toBe(false);
});

test("the visual viewport excludes a prompt hidden behind an iOS keyboard", () => {
  globalThis.window.visualViewport = { offsetTop: 0, offsetLeft: 0, width: 400, height: 200 };
  const behindKeyboard = {
    getBoundingClientRect: () => ({ top: 250, bottom: 350, left: 50, right: 150, width: 100, height: 100 }),
  };
  expect(isMeaningfullyInViewport(behindKeyboard)).toBe(false);
});

test("the fallback safety interval is paused while the document is hidden and rearms on foreground", () => {
  const originalSetInterval = globalThis.setInterval;
  const originalClearInterval = globalThis.clearInterval;
  let intervals = 0;
  globalThis.setInterval = () => { intervals++; return 1; };
  globalThis.clearInterval = () => {};
  globalThis.document.hidden = true;
  delete globalThis.IntersectionObserver;
  const harness = createHarness({
    sessionId: "s1", pending: { id: "ask", unseenGen: 7, serverInstance: "a" }, element: onScreenElement(),
  });
  try {
    harness.render();
    expect(intervals).toBe(0);
    globalThis.document.hidden = false;
    globalThis.document.emit("visibilitychange");
    harness.settle();
    expect(intervals).toBe(1);
  } finally {
    harness.unmount();
    globalThis.setInterval = originalSetInterval;
    globalThis.clearInterval = originalClearInterval;
  }
});
