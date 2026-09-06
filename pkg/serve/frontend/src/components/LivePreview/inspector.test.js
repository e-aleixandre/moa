import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const source = readFileSync(new URL("./inspector.js", import.meta.url), "utf8");

function bridgeAt(element, style = { overflowX: "visible", overflowY: "visible" }) {
  const listeners = {};
  const documentElement = { style: {} };
  const document = {
    currentScript: { getAttribute: () => "https://shell.test" },
    documentElement,
    addEventListener() {},
    removeEventListener() {},
    elementFromPoint: () => element,
    createElement: () => ({ style: {}, setAttribute() {} }),
  };
  const window = {
    parent: {},
    location: { href: "https://app.test/" },
    addEventListener(type, listener) { listeners[type] = listener; },
    removeEventListener() {},
    getComputedStyle: () => style,
    scrollBy() {},
  };
  vm.runInNewContext(source, { window, document, MouseEvent: class {} });
  return {
    relay(data) {
      listeners.message({ source: window.parent, origin: "https://shell.test", data });
    },
    window,
  };
}

test("moa-scroll bypasses smooth CSS for a nested scroller with instant behavior", () => {
  const calls = [];
  const nested = {
    getAttribute: () => null,
    parentElement: null,
    scrollHeight: 500,
    clientHeight: 100,
    scrollWidth: 100,
    clientWidth: 100,
    scrollBy: (options) => calls.push(options),
  };
  const { relay } = bridgeAt(nested, { overflowX: "visible", overflowY: "scroll", scrollBehavior: "smooth" });

  relay({ type: "moa-scroll", x: 20, y: 30, dx: 0, dy: 10 });

  expect(calls).toEqual([{ left: 0, top: 10, behavior: "instant" }]);
});

test("moa-scroll bypasses smooth CSS for the page root with the same fallbacks", () => {
  const calls = [];
  const rootTarget = {
    getAttribute: () => null,
    parentElement: null,
    scrollHeight: 100,
    clientHeight: 100,
    scrollWidth: 100,
    clientWidth: 100,
  };
  const { relay, window } = bridgeAt(rootTarget, { overflowX: "visible", overflowY: "visible", scrollBehavior: "smooth" });
  window.scrollBy = (options) => calls.push(options);
  relay({ type: "moa-scroll", x: 1, y: 1, dy: 0 });

  expect(calls).toEqual([{ left: 0, top: 0, behavior: "instant" }]);
});

// The zoomed chain: a tagged packet is answered with how much the chosen
// scroller ACTUALLY moved, so the shell can pan the frame by the rest.
function chainBridge(element, style = { overflowX: "visible", overflowY: "visible" }) {
  const listeners = {};
  const sent = [];
  const documentElement = { style: {} };
  const document = {
    currentScript: { getAttribute: () => "https://shell.test" },
    documentElement,
    addEventListener() {},
    removeEventListener() {},
    elementFromPoint: () => element,
    createElement: () => ({ style: {}, setAttribute() {} }),
  };
  const window = {
    parent: { postMessage: (msg) => sent.push(msg) },
    location: { href: "https://app.test/" },
    addEventListener(type, listener) { listeners[type] = listener; },
    removeEventListener() {},
    getComputedStyle: () => style,
    scrollX: 0,
    scrollY: 0,
    scrollBy(options) {
      window.scrollX = Math.max(0, window.scrollX + (options.left || 0));
      window.scrollY = Math.max(0, window.scrollY + (options.top || 0));
    },
  };
  vm.runInNewContext(source, { window, document, MouseEvent: class {} });
  return {
    relay(data) {
      listeners.message({ source: window.parent, origin: "https://shell.test", data });
    },
    sent,
    window,
  };
}

// A bounded scroller: it moves as far as it can and no further, which is what
// makes partial consumption real rather than assumed.
function boundedScroller(overrides = {}) {
  const el = {
    getAttribute: () => null,
    parentElement: null,
    scrollHeight: 500,
    clientHeight: 100,
    scrollWidth: 300,
    clientWidth: 100,
    scrollTop: 0,
    scrollLeft: 0,
    ...overrides,
  };
  el.scrollBy = (options) => {
    const maxTop = el.scrollHeight - el.clientHeight;
    const maxLeft = el.scrollWidth - el.clientWidth;
    el.scrollTop = Math.min(maxTop, Math.max(0, el.scrollTop + (options.top || 0)));
    el.scrollLeft = Math.min(maxLeft, Math.max(0, el.scrollLeft + (options.left || 0)));
  };
  return el;
}

test("the bridge announces that it answers scroll packets", () => {
  const { sent, relay } = chainBridge(boundedScroller());
  expect(sent[0]).toEqual({ type: "moa-ready", chain: true });
  relay({ type: "moa-hello" });
  expect(sent[1]).toEqual({ type: "moa-ready", chain: true });
});

test("a nested scroller in mid-range answers with the full consumption it made", () => {
  const nested = boundedScroller({ scrollTop: 200 });
  const { relay, sent } = chainBridge(nested, { overflowX: "visible", overflowY: "scroll" });

  relay({ type: "moa-scroll", id: "s1", x: 10, y: 10, dx: 0, dy: 50, reset: true });

  expect(nested.scrollTop).toBe(250);
  expect(sent.at(-1)).toEqual({ type: "moa-scrolled", id: "s1", dx: 0, dy: 50 });
});

test("a scroller at its top answers zero for an upward request, with the same id", () => {
  const nested = boundedScroller({ scrollTop: 0 });
  const { relay, sent } = chainBridge(nested, { overflowX: "visible", overflowY: "scroll" });

  relay({ type: "moa-scroll", id: "s7", x: 10, y: 10, dx: 0, dy: -150, reset: true });

  expect(sent.at(-1)).toEqual({ type: "moa-scrolled", id: "s7", dx: 0, dy: 0 });
});

test("a scroller near its edge answers with exactly the part it could take", () => {
  const nested = boundedScroller({ scrollTop: 30 });
  const { relay, sent } = chainBridge(nested, { overflowX: "visible", overflowY: "scroll" });

  relay({ type: "moa-scroll", id: "s2", x: 10, y: 10, dx: 0, dy: -100, reset: true });

  expect(nested.scrollTop).toBe(0);
  expect(sent.at(-1)).toEqual({ type: "moa-scrolled", id: "s2", dx: 0, dy: -30 });
});

test("the page root answers about the window, per axis", () => {
  const root = { getAttribute: () => null, parentElement: null, scrollHeight: 100, clientHeight: 100, scrollWidth: 100, clientWidth: 100 };
  const { relay, sent, window } = chainBridge(root);
  window.scrollY = 40;

  relay({ type: "moa-scroll", id: "s3", x: 1, y: 1, dx: 25, dy: -70, reset: true });

  expect(sent.at(-1)).toEqual({ type: "moa-scrolled", id: "s3", dx: 25, dy: -40 });
});

test("a chained gesture keeps its target, root included, once it has been picked", () => {
  const root = { getAttribute: () => null, parentElement: null, scrollHeight: 100, clientHeight: 100, scrollWidth: 100, clientWidth: 100 };
  const { relay, sent, window } = chainBridge(root);

  relay({ type: "moa-scroll", id: "s4", x: 1, y: 1, dx: 0, dy: 10, reset: true });
  // A nested scroller has slid under the finger — the gesture keeps the root.
  window.getComputedStyle = () => ({ overflowX: "visible", overflowY: "scroll" });
  relay({ type: "moa-scroll", id: "s5", x: 1, y: 1, dx: 0, dy: 10, reset: false });

  expect(window.scrollY).toBe(20);
  expect(sent.at(-1)).toEqual({ type: "moa-scrolled", id: "s5", dx: 0, dy: 10 });
});

test("smooth CSS is still overridden for a chained packet", () => {
  const calls = [];
  const nested = boundedScroller({ scrollTop: 100 });
  const scrollBy = nested.scrollBy;
  nested.scrollBy = (options) => { calls.push(options); scrollBy(options); };
  const { relay } = chainBridge(nested, { overflowX: "visible", overflowY: "scroll", scrollBehavior: "smooth" });

  relay({ type: "moa-scroll", id: "s6", x: 10, y: 10, dx: 0, dy: 10, reset: true });

  expect(calls).toEqual([{ left: 0, top: 10, behavior: "instant" }]);
});

// An untagged packet is the zoom-1 contract: it scrolls and says nothing.
test("an untagged packet still scrolls and is answered with nothing", () => {
  const nested = boundedScroller({ scrollTop: 100 });
  const { relay, sent } = chainBridge(nested, { overflowX: "visible", overflowY: "scroll" });
  const before = sent.length;

  relay({ type: "moa-scroll", x: 10, y: 10, dx: 0, dy: 10, reset: true });

  expect(nested.scrollTop).toBe(110);
  expect(sent.length).toBe(before);
});

// ── Back: the app's OWN Navigation API, and nothing else ────────────────────
//
// The bridge is run against the real inspector source with a fake `navigation`
// on its window, so what is asserted is what the file actually does: it reads
// its own frame's entry list, and never reaches for `history`.
function navBridge({ navigation = null } = {}) {
  const listeners = {};
  const sent = [];
  const historyCalls = [];
  const document = {
    currentScript: { getAttribute: () => "https://shell.test" },
    documentElement: { style: {} },
    addEventListener() {},
    removeEventListener() {},
    elementFromPoint: () => null,
    createElement: () => ({ style: {}, setAttribute() {} }),
  };
  const window = {
    parent: { postMessage: (msg) => sent.push(msg) },
    location: { href: "https://app.test/" },
    navigation,
    // Legacy traversal is joint with moa's own session history: if the bridge
    // ever touched it, these would record it.
    history: {
      get length() { historyCalls.push("length"); return 5; },
      back: () => historyCalls.push("back"),
      go: () => historyCalls.push("go"),
    },
    addEventListener(type, listener) { listeners[type] = listener; },
    removeEventListener() {},
    getComputedStyle: () => ({ overflowX: "visible", overflowY: "visible" }),
    scrollBy() {},
  };
  vm.runInNewContext(source, { window, document, MouseEvent: class {} });
  return {
    relay(data, { origin = "https://shell.test", source: from = window.parent } = {}) {
      listeners.message({ source: from, origin, data });
    },
    sent,
    historyCalls,
  };
}

function fakeNavigation({ canGoBack = false, back } = {}) {
  const traverse = back || (() => ({ committed: Promise.resolve(), finished: Promise.resolve() }));
  const nav = {
    canGoBack,
    listeners: {},
    backCalls: 0,
    addEventListener(type, listener) { nav.listeners[type] = listener; },
    removeEventListener() {},
    back() {
      nav.backCalls += 1;
      return traverse();
    },
  };
  return nav;
}

const reports = (sent) => sent.filter((msg) => msg.type === "moa-preview-navigation");

test("the hello mints the epoch and the answer reports this frame's own canGoBack", () => {
  const nav = fakeNavigation({ canGoBack: true });
  const { relay, sent, historyCalls } = navBridge({ navigation: nav });

  relay({ type: "moa-hello", navigationEpoch: 3 });

  expect(reports(sent)).toEqual([{ type: "moa-preview-navigation", navigationEpoch: 3, supported: true, canGoBack: true }]);
  expect(historyCalls).toEqual([]);
});

test("a browser without the Navigation API answers supported false and never goes back", () => {
  const { relay, sent, historyCalls } = navBridge({ navigation: null });

  relay({ type: "moa-hello", navigationEpoch: 1 });
  relay({ type: "moa-preview-back", navigationEpoch: 1 });

  expect(reports(sent)).toEqual([
    { type: "moa-preview-navigation", navigationEpoch: 1, supported: false, canGoBack: false },
    { type: "moa-preview-navigation", navigationEpoch: 1, supported: false, canGoBack: false },
  ]);
  expect(historyCalls).toEqual([]);
});

test("a partial Navigation API is treated as unsupported", () => {
  const { relay, sent } = navBridge({ navigation: { canGoBack: true, addEventListener() {} } });

  relay({ type: "moa-hello", navigationEpoch: 1 });

  expect(reports(sent).at(-1).supported).toBe(false);
});

test("an SPA route change reports the fresh value without a new document", () => {
  const nav = fakeNavigation({ canGoBack: false });
  const { relay, sent } = navBridge({ navigation: nav });
  relay({ type: "moa-hello", navigationEpoch: 7 });

  nav.canGoBack = true;
  nav.listeners.currententrychange();

  expect(reports(sent).at(-1)).toEqual({ type: "moa-preview-navigation", navigationEpoch: 7, supported: true, canGoBack: true });
});

test("the back command traverses this frame's own navigation, never history", () => {
  const nav = fakeNavigation({ canGoBack: true });
  const { relay, historyCalls } = navBridge({ navigation: nav });
  relay({ type: "moa-hello", navigationEpoch: 2 });

  relay({ type: "moa-preview-back", navigationEpoch: 2 });

  expect(nav.backCalls).toBe(1);
  expect(historyCalls).toEqual([]);
});

test("a command minted before the current document is ignored", () => {
  const nav = fakeNavigation({ canGoBack: true });
  const { relay } = navBridge({ navigation: nav });
  relay({ type: "moa-hello", navigationEpoch: 5 });

  relay({ type: "moa-preview-back", navigationEpoch: 4 });

  expect(nav.backCalls).toBe(0);
});

test("a command from the wrong origin or window is ignored", () => {
  const nav = fakeNavigation({ canGoBack: true });
  const { relay } = navBridge({ navigation: nav });
  relay({ type: "moa-hello", navigationEpoch: 1 });

  relay({ type: "moa-preview-back", navigationEpoch: 1 }, { origin: "https://evil.test" });
  relay({ type: "moa-preview-back", navigationEpoch: 1 }, { source: {} });

  expect(nav.backCalls).toBe(0);
});

test("a raced back rechecks canGoBack and reports instead of traversing", () => {
  const nav = fakeNavigation({ canGoBack: true });
  const { relay, sent } = navBridge({ navigation: nav });
  relay({ type: "moa-hello", navigationEpoch: 1 });

  // The last entry was consumed between the report and the click.
  nav.canGoBack = false;
  relay({ type: "moa-preview-back", navigationEpoch: 1 });

  expect(nav.backCalls).toBe(0);
  expect(reports(sent).at(-1)).toEqual({ type: "moa-preview-navigation", navigationEpoch: 1, supported: true, canGoBack: false });
});

test("a rejected traversal reports fresh status so the shell leaves busy", async () => {
  const nav = fakeNavigation({
    canGoBack: true,
    back: () => ({ committed: Promise.reject(new Error("cancelled")), finished: Promise.reject(new Error("cancelled")) }),
  });
  const { relay, sent } = navBridge({ navigation: nav });
  relay({ type: "moa-hello", navigationEpoch: 1 });

  relay({ type: "moa-preview-back", navigationEpoch: 1 });
  await new Promise((resolve) => setTimeout(resolve, 0));

  expect(reports(sent).at(-1)).toEqual({ type: "moa-preview-navigation", navigationEpoch: 1, supported: true, canGoBack: true });
});

test("SecurityError is reported to the parent on every rejected native traversal", async () => {
  const securityError = () => Object.assign(new Error("Invalid state"), { name: "SecurityError" });
  const nav = fakeNavigation({
    canGoBack: true,
    back: () => ({ committed: Promise.reject(securityError()), finished: Promise.reject(securityError()) }),
  });
  const { relay, sent } = navBridge({ navigation: nav });
  relay({ type: "moa-hello", navigationEpoch: 9 });

  relay({ type: "moa-preview-back", navigationEpoch: 9 });
  await new Promise((resolve) => setTimeout(resolve, 0));
  relay({ type: "moa-preview-back", navigationEpoch: 9 });
  await new Promise((resolve) => setTimeout(resolve, 0));

  const securityReports = reports(sent).filter((report) => report.backError === "SecurityError");
  expect(securityReports).toHaveLength(4);
  expect(securityReports.every((report) => report.navigationEpoch === 9 && report.canGoBack === true)).toBe(true);
});

test("a throwing back() does not fall through to legacy traversal", () => {
  const nav = fakeNavigation({ canGoBack: true, back: () => { throw new Error("no"); } });
  const { relay, sent, historyCalls } = navBridge({ navigation: nav });
  relay({ type: "moa-hello", navigationEpoch: 1 });

  relay({ type: "moa-preview-back", navigationEpoch: 1 });

  expect(historyCalls).toEqual([]);
  expect(reports(sent).at(-1).canGoBack).toBe(true);
});

test("an older shell that never says hello gets no navigation report at all", () => {
  const nav = fakeNavigation({ canGoBack: true });
  const { relay, sent } = navBridge({ navigation: nav });

  relay({ type: "moa-hello" });
  nav.listeners.currententrychange();

  expect(reports(sent)).toEqual([]);
  expect(sent.at(-1)).toEqual({ type: "moa-ready", chain: true });
});
