import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { appToStage, chainPan, panBy, wheelFactor, zoomAt, IDENTITY } from "./zoom.js";

// The DESKTOP half of the bridge, run against the REAL inspector source: a
// mouse or trackpad over the app is inside the iframe's document, so everything
// the shell can know about it is what this file chooses to send.
const source = readFileSync(new URL("./inspector.js", import.meta.url), "utf8");

const stage = { w: 900, h: 700 };
const frame = { base: 1, w: 900, h: 700, stage };

function scroller({ top = 0, left = 0, height = 4000, width = 900, clientHeight = 700, clientWidth = 900, integer = false } = {}) {
  const el = {
    getAttribute: () => null,
    parentElement: null,
    scrollHeight: height,
    clientHeight,
    scrollWidth: width,
    clientWidth,
    scrollTop: top,
    scrollLeft: left,
    calls: [],
  };
  el.scrollBy = (options) => {
    el.calls.push(options);
    const top = el.scrollTop + (options.top || 0);
    const left = el.scrollLeft + (options.left || 0);
    el.scrollTop = Math.min(el.scrollHeight - el.clientHeight, Math.max(0, integer ? Math.trunc(top) : top));
    el.scrollLeft = Math.min(el.scrollWidth - el.clientWidth, Math.max(0, integer ? Math.trunc(left) : left));
  };
  return el;
}

// desktop — one inspector in a document, with the listeners it installs on
// `window` and `document` captured so a wheel or a key can be delivered exactly
// where the browser would deliver it.
function desktop({ target = null, style = { overflowX: "visible", overflowY: "visible" }, activeElement = null, pageHeight = 700 } = {}) {
  const sent = [];
  const winListeners = {};
  const docListeners = {};
  const pageCalls = [];
  const document = {
    currentScript: { getAttribute: () => "https://shell.test" },
    documentElement: { style: {}, clientHeight: 700, clientWidth: 900, scrollHeight: pageHeight, scrollWidth: 900 },
    activeElement,
    addEventListener(type, listener) { (docListeners[type] ||= []).push(listener); },
    removeEventListener() {},
    elementFromPoint: () => target,
    createElement: () => ({ style: {}, setAttribute() {} }),
  };
  const window = {
    parent: { postMessage: (msg) => sent.push(msg) },
    location: { href: "https://app.test/" },
    innerHeight: 700,
    scrollX: 0,
    scrollY: 0,
    addEventListener(type, listener) { (winListeners[type] ||= []).push(listener); },
    removeEventListener() {},
    getComputedStyle: (el) => typeof style === "function" ? style(el) : style,
    scrollBy(options) {
      pageCalls.push(options);
      window.scrollX = Math.max(0, window.scrollX + (options.left || 0));
      window.scrollY = Math.min(Math.max(0, pageHeight - 700), Math.max(0, window.scrollY + (options.top || 0)));
    },
  };
  vm.runInNewContext(source, { window, document, MouseEvent: class {}, Number });

  const fire = (map, type, event) => (map[type] || []).forEach((l) => l(event));
  return {
    sent,
    window,
    document,
    pageCalls,
    view(data) {
      fire(winListeners, "message", { source: window.parent, origin: "https://shell.test", data });
    },
    wheel(event) {
      const e = { deltaX: 0, deltaY: 0, deltaMode: 0, ctrlKey: false, clientX: 10, clientY: 10, defaultPrevented: false, ...event };
      e.preventDefault = () => { e.defaultPrevented = true; };
      fire(winListeners, "wheel", e);
      return e;
    },
    gesture(type, event = {}) {
      const e = { scale: 1, ...event };
      e.preventDefault = () => { e.defaultPrevented = true; };
      fire(winListeners, type, e);
      return e;
    },
    key(type, event) {
      const e = { repeat: false, ...event };
      e.preventDefault = () => { e.prevented = true; };
      e.stopPropagation = () => { e.stopped = true; };
      fire(docListeners, type, e);
      return e;
    },
    blur() { fire(winListeners, "blur", {}); },
  };
}

const announce = (app, zoom, epoch = 1) => app.view({ type: "moa-view", epoch, zoom, zoomed: zoom !== 1 });

// ── The bridge stays quiet until moa introduces itself ──────────────────────

test("an app that was never told about a view relays no wheel and no key", () => {
  const app = desktop({ target: scroller() });
  const before = app.sent.length;

  const e = app.wheel({ deltaY: 120, ctrlKey: true });
  app.key("keydown", { key: " " });

  expect(app.sent.length).toBe(before);
  // And it did not swallow the app's own wheel either.
  expect(e.defaultPrevented).toBe(false);
});

// ── Ctrl + wheel, which is also how a trackpad pinch arrives ────────────────
// Chromium and Firefox deliver a trackpad pinch as a wheel with ctrlKey.

test("ctrl+wheel is relayed with the pointer, at zoom 1, and the app is not scrolled", () => {
  const target = scroller();
  const app = desktop({ target });
  announce(app, 1);

  const e = app.wheel({ deltaY: -100, ctrlKey: true, clientX: 300, clientY: 220 });

  expect(e.defaultPrevented).toBe(true);
  expect(app.sent.at(-1)).toEqual({ type: "moa-wheel-zoom", epoch: 1, deltaY: -100, x: 300, y: 220 });
  expect(target.calls).toEqual([]);
});

test("WebKit gesture scale is relayed incrementally and suppresses duplicate ctrl-wheel", () => {
  const app = desktop({ target: scroller() });
  announce(app, 1);
  const start = app.gesture("gesturestart", { scale: 1, clientX: 300, clientY: 220 });
  const change = app.gesture("gesturechange", { scale: 1.2, clientX: 300, clientY: 220 });
  expect(start.defaultPrevented).toBe(true);
  expect(change.defaultPrevented).toBe(true);
  expect(app.sent.at(-1)).toEqual({ type: "moa-wheel-zoom", epoch: 1, factor: 1.2, x: 300, y: 220 });
  const wheel = app.wheel({ deltaY: -100, ctrlKey: true });
  expect(wheel.defaultPrevented).toBe(true);
  expect(app.sent.filter((m) => m.type === "moa-wheel-zoom")).toHaveLength(1);
  app.gesture("gestureend");
  app.gesture("gesturechange", { scale: 1.3 });
  expect(app.sent.filter((m) => m.type === "moa-wheel-zoom")).toHaveLength(1);
});

test("WebKit gesture without coordinates uses a finite frame-centre fallback", () => {
  const app = desktop({ target: scroller() });
  announce(app, 1);
  app.gesture("gesturestart", { scale: 1 });
  app.gesture("gesturechange", { scale: 1.1 });
  expect(app.sent.at(-1)).toEqual({ type: "moa-wheel-zoom", epoch: 1, factor: 1.1, x: 450, y: 350 });
});

test("the pointer's app pixel stays under the pointer while ctrl+wheel zooms", () => {
  const app = desktop({ target: scroller() });
  announce(app, 1);
  app.wheel({ deltaY: -100, ctrlKey: true, clientX: 300, clientY: 220 });
  const packet = app.sent.at(-1);

  // What the shell does with that packet.
  const anchor = appToStage(IDENTITY, frame, packet.x, packet.y);
  const next = zoomAt(IDENTITY, wheelFactor(packet.deltaY), anchor, frame, stage);

  expect(next.zoom).toBeGreaterThan(1);
  expect(next.x + packet.x * next.zoom).toBeCloseTo(anchor.x, 5);
  expect(next.y + packet.y * next.zoom).toBeCloseTo(anchor.y, 5);
});

test("a rapid ctrl+wheel burst is applied in full — one epoch, thirty events", () => {
  const app = desktop({ target: scroller() });
  announce(app, 1);

  // The shell only re-announces the ZOOM as the burst is applied; the epoch is
  // the same throughout, which is what stops the burst being thrown away.
  let view = IDENTITY;
  for (let i = 0; i < 30; i++) {
    app.wheel({ deltaY: -8, ctrlKey: true, clientX: 450, clientY: 350 });
    const packet = app.sent.at(-1);
    expect(packet.epoch).toBe(1);
    const anchor = appToStage(view, frame, packet.x, packet.y);
    view = zoomAt(view, wheelFactor(packet.deltaY), anchor, frame, stage);
    announce(app, view.zoom, 1);
  }

  const relayed = app.sent.filter((m) => m.type === "moa-wheel-zoom");
  expect(relayed.length).toBe(30);
  // exp(8/300) per step, 30 steps — every one of them counted.
  expect(view.zoom).toBeCloseTo(Math.exp((8 * 30) / 300), 5);
});

test("a very large single delta cannot jump the whole zoom range", () => {
  expect(wheelFactor(-100000)).toBeCloseTo(Math.exp(200 / 300), 5);
  expect(wheelFactor(100000)).toBeCloseTo(Math.exp(-200 / 300), 5);
  expect(wheelFactor(NaN)).toBe(1);
});

// ── Ordinary wheel: the app first, the residual to the frame ────────────────

test("at zoom 1 an ordinary wheel is left entirely to the app", () => {
  const target = scroller();
  const app = desktop({ target, style: { overflowX: "visible", overflowY: "scroll" } });
  announce(app, 1);
  const before = app.sent.length;

  const e = app.wheel({ deltaY: 120 });

  expect(e.defaultPrevented).toBe(false);
  expect(target.calls).toEqual([]);
  expect(app.sent.length).toBe(before);
});

test("zoomed, a scroller with room takes the whole wheel and the shell hears nothing", () => {
  const target = scroller({ top: 500, clientHeight: 300 });
  const app = desktop({ target, style: { overflowX: "visible", overflowY: "scroll" } });
  announce(app, 2);
  const before = app.sent.length;

  const e = app.wheel({ deltaY: 120 });

  expect(e.defaultPrevented).toBe(true);
  expect(target.scrollTop).toBe(620);
  expect(target.calls).toEqual([{ left: 0, top: 120, behavior: "instant" }]);
  expect(app.sent.length).toBe(before);
});

test("zoomed Shift+wheel uses the browser's horizontal-wheel convention", () => {
  const target = scroller({ top: 500, width: 2000, clientWidth: 900, clientHeight: 300 });
  const app = desktop({ target, style: { overflowX: "scroll", overflowY: "scroll" } });
  announce(app, 2);
  const before = app.sent.length;

  const e = app.wheel({ deltaX: 0, deltaY: 120, shiftKey: true });

  expect(e.defaultPrevented).toBe(true);
  expect(target.scrollLeft).toBe(120);
  expect(target.scrollTop).toBe(500);
  expect(target.calls).toEqual([{ left: 120, top: 0, behavior: "instant" }]);
  expect(app.sent.length).toBe(before);
});

test("eight quarter-pixel wheels scroll an integer list without moving outer pan", () => {
  const target = scroller({ height: 1520, clientHeight: 600, integer: true });
  const app = desktop({ target, style: { overflowX: "visible", overflowY: "scroll" } });
  announce(app, 1.94773);
  const before = app.sent.length;

  for (let i = 0; i < 8; i++) app.wheel({ deltaY: 0.25 });

  expect(target.scrollTop).toBe(2);
  expect(app.window.scrollY).toBe(0);
  expect(app.sent.slice(before).filter((m) => m.type === "moa-wheel-pan")).toEqual([]);
});

test("fractional wheels hand only movement beyond an integer list boundary to outer pan", () => {
  const target = scroller({ height: 603, clientHeight: 600, integer: true });
  const app = desktop({ target, style: { overflowX: "visible", overflowY: "scroll" } });
  announce(app, 2);
  let view = { zoom: 2, x: 0, y: -250 };

  for (let i = 0; i < 2; i++) {
    app.wheel({ deltaY: 3.333 });
    const packet = app.sent.at(-1);
    view = chainPan(view, { dx: packet.rdx, dy: packet.rdy, scale: 2 }, packet, frame, stage);
  }

  expect(target.scrollTop).toBe(3);
  expect(app.window.scrollY).toBe(0);
  expect(view.y).toBeCloseTo(-257.332, 8);
});

test("relayed scrolling is instant: a smooth scroller cannot hide its consumption", () => {
  const target = scroller({ top: 500, clientHeight: 300 });
  const app = desktop({ target, style: { overflowX: "visible", overflowY: "scroll", scrollBehavior: "smooth" } });
  announce(app, 2);

  app.wheel({ deltaY: 40 });

  expect(target.calls.every((c) => c.behavior === "instant")).toBe(true);
});

test("only the part the app could not take is reported, per axis", () => {
  // 30px of room downward, plenty of room rightward.
  const target = scroller({ top: 470, height: 800, clientHeight: 300, width: 2000, clientWidth: 900 });
  const app = desktop({ target, style: { overflowX: "scroll", overflowY: "scroll" }, pageHeight: 700 });
  announce(app, 2);

  app.wheel({ deltaX: 50, deltaY: 100 });

  expect(target.scrollTop).toBe(500);
  expect(target.scrollLeft).toBe(50);
  expect(app.sent.at(-1)).toEqual({
    type: "moa-wheel-pan", epoch: 1, zoom: 2, rdx: 50, rdy: 100, dx: 50, dy: 30,
  });

  // And what the shell does with it: the consumed axis does not move the pan,
  // the boundary axis moves it by the residual at the scale it was measured at.
  const view = { zoom: 2, x: -100, y: -250 };
  const packet = app.sent.at(-1);
  const next = chainPan(view, { dx: packet.rdx, dy: packet.rdy, scale: 2 }, packet, frame, stage);
  expect(next.x).toBe(-100);
  expect(next.y).toBe(-250 - 70 * 2);
});

test("the page takes what a nested scroller at its edge could not", () => {
  const target = scroller({ top: 4000 - 300, clientHeight: 300 });
  const app = desktop({ target, style: { overflowX: "visible", overflowY: "scroll" }, pageHeight: 2000 });
  announce(app, 2);

  app.wheel({ deltaY: 100 });

  expect(app.window.scrollY).toBe(100);
  expect(app.pageCalls).toEqual([{ left: 0, top: 100, behavior: "instant" }]);
  // Everything was consumed between the two, so there is nothing to pan.
  expect(app.sent.filter((m) => m.type === "moa-wheel-pan")).toEqual([]);
});

test("a scrollable parent takes residual before the page or preview pan", () => {
  const parent = scroller({ top: 100, height: 2000, clientHeight: 500 });
  const inner = scroller({ top: 700, height: 1000, clientHeight: 300 });
  inner.parentElement = parent;
  const app = desktop({ target: inner, style: (el) => ({ overflowX: "visible", overflowY: el === inner || el === parent ? "scroll" : "visible" }), pageHeight: 700 });
  announce(app, 2);
  app.wheel({ deltaY: 100 });
  expect(inner.scrollTop).toBe(700);
  expect(parent.scrollTop).toBe(200);
  expect(app.pageCalls).toEqual([]);
  expect(app.sent.filter((m) => m.type === "moa-wheel-pan")).toEqual([]);
});

test("a snapping child that overshoots does not send opposite movement to the page", () => {
  const target = scroller({ top: 100, height: 2000, clientHeight: 500 });
  target.scrollBy = (options) => {
    target.calls.push(options);
    target.scrollTop += 180;
  };
  const app = desktop({ target, style: { overflowX: "visible", overflowY: "scroll" }, pageHeight: 2000 });
  app.window.scrollY = 500;
  announce(app, 2);

  app.wheel({ deltaY: 100 });

  expect(target.scrollTop).toBe(280);
  expect(app.window.scrollY).toBe(500);
  expect(app.pageCalls).toEqual([]);
  expect(app.sent.filter((m) => m.type === "moa-wheel-pan")).toEqual([]);
});

test("a wheel the app has already handled itself is not taken", () => {
  const target = scroller({ top: 0, clientHeight: 300 });
  const app = desktop({ target, style: { overflowX: "visible", overflowY: "scroll" } });
  announce(app, 2);
  const before = app.sent.length;

  app.wheel({ deltaY: 100, defaultPrevented: true });

  expect(target.calls).toEqual([]);
  expect(app.sent.length).toBe(before);
});

test("a wheel is never applied twice: the default is cancelled before anything moves", () => {
  const target = scroller({ top: 0, clientHeight: 300 });
  const app = desktop({ target, style: { overflowX: "visible", overflowY: "scroll" } });
  announce(app, 2);

  const e = app.wheel({ deltaY: 100 });

  expect(e.defaultPrevented).toBe(true);
  expect(target.calls.length).toBe(1);
  expect(target.scrollTop).toBe(100);
});

test("fractional touch relay accumulates integer DOM rounding without panning", () => {
  const target = scroller({ height: 1000, clientHeight: 100, integer: true });
  const app = desktop({ target, style: { overflowX: "visible", overflowY: "scroll" } });
  for (let i = 0; i < 29; i++) app.view({ type: "moa-scroll", id: i, reset: i === 0, x: 10, y: 10, dx: 0, dy: 3.333 });
  const replies = app.sent.filter((m) => m.type === "moa-scrolled");
  expect(replies).toHaveLength(29);
  expect(replies.reduce((sum, m) => sum + m.dy, 0)).toBeCloseTo(29 * 3.333, 8);
  expect(target.scrollTop).toBe(96);
});

test("eight quarter-pixel touch packets scroll the integer DOM without moving outer pan", () => {
  const target = scroller({ height: 1000, clientHeight: 100, integer: true });
  const app = desktop({ target, style: { overflowX: "visible", overflowY: "scroll" } });
  let view = { zoom: 2, x: 0, y: -250 };

  for (let i = 0; i < 8; i++) {
    app.view({ type: "moa-scroll", id: i, reset: i === 0, x: 10, y: 10, dx: 0, dy: 0.25 });
    view = chainPan(view, { dx: 0, dy: 0.25, scale: 2 }, app.sent.at(-1), frame, stage);
  }

  expect(target.scrollTop).toBe(2);
  expect(view.y).toBe(-250);
});

test("fractional packets hand only the part beyond an integer DOM boundary to outer pan", () => {
  const target = scroller({ height: 103, clientHeight: 100, integer: true });
  const app = desktop({ target, style: { overflowX: "visible", overflowY: "scroll" } });
  let view = { zoom: 2, x: 0, y: -250 };

  for (let i = 0; i < 2; i++) {
    app.view({ type: "moa-scroll", id: i, reset: i === 0, x: 10, y: 10, dx: 0, dy: 3.333 });
    view = chainPan(view, { dx: 0, dy: 3.333, scale: 2 }, app.sent.at(-1), frame, stage);
  }

  expect(target.scrollTop).toBe(3);
  expect(view.y).toBeCloseTo(-257.332, 8);
});

test("fractional relay resets on reversal and reports carried debt at a real boundary", () => {
  const target = scroller({ height: 110, clientHeight: 100, integer: true });
  const app = desktop({ target, style: { overflowX: "visible", overflowY: "scroll" } });
  for (let i = 0; i < 3; i++) app.view({ type: "moa-scroll", id: i, reset: i === 0, x: 10, y: 10, dx: 0, dy: 3.333 });
  app.view({ type: "moa-scroll", id: 3, x: 10, y: 10, dx: 0, dy: 3.333 });
  // The prior .999px carry is included: 4.332 requested from the integer DOM,
  // one pixel fits, so exactly 3.332px reaches outer pan.
  expect(app.sent.at(-1).dy).toBeCloseTo(0.001, 8);
  app.view({ type: "moa-scroll", id: 4, reset: true, x: 10, y: 10, dx: 0, dy: -3.333 });
  expect(app.sent.at(-1).dy).toBeCloseTo(-3.333, 8);
});

// ── deltaMode ───────────────────────────────────────────────────────────────

test("line and page deltas become pixels before anything is measured", () => {
  const lines = scroller({ top: 0, height: 10000, clientHeight: 300 });
  const app = desktop({ target: lines, style: { overflowX: "visible", overflowY: "scroll" } });
  announce(app, 2);
  app.wheel({ deltaY: 3, deltaMode: 1 });
  expect(lines.scrollTop).toBe(48);

  const pages = scroller({ top: 0, height: 10000, clientHeight: 500 });
  const app2 = desktop({ target: pages, style: { overflowX: "visible", overflowY: "scroll" } });
  announce(app2, 2);
  app2.wheel({ deltaY: 1, deltaMode: 2 });
  expect(pages.scrollTop).toBe(500);
});

// ── Space ───────────────────────────────────────────────────────────────────

test("Space over a zoomed app is relayed as a pan gesture and released on keyup", () => {
  const app = desktop({ target: scroller() });
  announce(app, 2);

  const down = app.key("keydown", { key: " " });
  expect(down.prevented).toBe(true);
  expect(app.sent.at(-1)).toEqual({ type: "moa-space", epoch: 1, down: true });

  app.key("keyup", { key: " " });
  expect(app.sent.at(-1)).toEqual({ type: "moa-space", epoch: 1, down: false });
});

test("Space is never taken where text is typed", () => {
  for (const activeElement of [
    { nodeType: 1, tagName: "INPUT" },
    { nodeType: 1, tagName: "TEXTAREA" },
    { nodeType: 1, tagName: "SELECT" },
    { nodeType: 1, tagName: "DIV", isContentEditable: true },
  ]) {
    const app = desktop({ target: scroller(), activeElement });
    announce(app, 2);
    const before = app.sent.length;

    const e = app.key("keydown", { key: " " });

    expect(e.prevented).toBeUndefined();
    expect(app.sent.length).toBe(before);
  }
});

test("Space is not a pan when there is nothing to pan", () => {
  const app = desktop({ target: scroller() });
  announce(app, 1);
  const before = app.sent.length;

  const e = app.key("keydown", { key: " " });

  expect(e.prevented).toBeUndefined();
  expect(app.sent.length).toBe(before);
});

test("a repeat is not a second gesture", () => {
  const app = desktop({ target: scroller() });
  announce(app, 2);
  app.key("keydown", { key: " " });
  const after = app.sent.length;

  app.key("keydown", { key: " ", repeat: true });

  expect(app.sent.length).toBe(after);
});

test("a Space held while the app loses focus is released", () => {
  const app = desktop({ target: scroller() });
  announce(app, 2);
  app.key("keydown", { key: " " });

  app.blur();

  expect(app.sent.at(-1)).toEqual({ type: "moa-space", epoch: 1, down: false });
});

test("a new document, width or stage size cancels a held Space", () => {
  const app = desktop({ target: scroller() });
  announce(app, 2);
  app.key("keydown", { key: " " });

  announce(app, 2, 2);

  expect(app.sent.at(-1)).toEqual({ type: "moa-space", epoch: 1, down: false });
});

test("dropping back to zoom 1 ends a held pan", () => {
  const app = desktop({ target: scroller() });
  announce(app, 2);
  app.key("keydown", { key: " " });

  announce(app, 1, 1);

  expect(app.sent.at(-1)).toEqual({ type: "moa-space", epoch: 1, down: false });
});

test("Escape still closes the preview — the new key handling did not take it", () => {
  const app = desktop({ target: scroller() });
  announce(app, 2);

  app.key("keydown", { key: "Escape" });

  expect(app.sent.some((m) => m.type === "moa-escape")).toBe(true);
});

// ── The shell's own pan math for the Space drag ─────────────────────────────

test("a Space drag moves the frame by the pointer travel, within bounds", () => {
  const view = { zoom: 2, x: -200, y: -300 };
  expect(panBy(view, -40, 25, frame, stage)).toEqual({ zoom: 2, x: -240, y: -275 });
  // And it cannot drag the app off the stage.
  expect(panBy(view, 999, 999, frame, stage)).toEqual({ zoom: 2, x: 0, y: 0 });
  expect(panBy(view, -9999, -9999, frame, stage)).toEqual({ zoom: 2, x: -900, y: -700 });
});

test("appToStage is the only conversion the desktop bridge needs", () => {
  expect(appToStage({ zoom: 2, x: -100, y: -50 }, frame, 300, 220)).toEqual({ x: 500, y: 390 });
  expect(appToStage(IDENTITY, { base: 0.5, w: 900, h: 700 }, 300, 220)).toEqual({ x: 150, y: 110 });
});
