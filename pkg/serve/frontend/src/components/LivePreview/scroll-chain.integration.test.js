import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { chainPan, IDENTITY } from "./zoom.js";
import { createScrollChain } from "./scroll-chain.js";

// The two halves of the zoomed chain meeting: the REAL inspector answering real
// DOM-ish scrollers, and the shell's own bookkeeping and pan math reading those
// answers. The component only glues these together; what could actually go
// wrong — who consumes what, and which answers still count — lives here.
const source = readFileSync(new URL("./inspector.js", import.meta.url), "utf8");

const stage = { w: 390, h: 800 };
const frame = { base: 1, w: 390, h: 800, stage };

function scroller({ top = 0, left = 0, height = 4000, width = 390 } = {}) {
  const el = {
    getAttribute: () => null,
    parentElement: null,
    scrollHeight: height,
    clientHeight: 800,
    scrollWidth: width,
    clientWidth: 390,
    scrollTop: top,
    scrollLeft: left,
  };
  el.scrollBy = (options) => {
    el.scrollTop = Math.min(el.scrollHeight - el.clientHeight, Math.max(0, el.scrollTop + (options.top || 0)));
    el.scrollLeft = Math.min(el.scrollWidth - el.clientWidth, Math.max(0, el.scrollLeft + (options.left || 0)));
  };
  return el;
}

// app — an inspector running over one scroller, wired to a shell that owns a
// view, a chain and the pan math. `drag` is one flushed packet: stage px of
// finger travel, exactly what the overlay sends.
function app({ target, nested = false, view = { zoom: 2, x: 0, y: -250 } } = {}) {
  const listeners = {};
  const shell = { view, chain: createScrollChain(), chainable: false };
  const document = {
    currentScript: { getAttribute: () => "https://shell.test" },
    documentElement: { style: {} },
    addEventListener() {},
    removeEventListener() {},
    elementFromPoint: () => target,
    createElement: () => ({ style: {}, setAttribute() {} }),
  };
  const window = {
    parent: {
      postMessage(msg) {
        if (msg.type === "moa-ready") { shell.chainable = msg.chain === true; return; }
        if (msg.type !== "moa-scrolled") return;
        const request = shell.chain.resolve(msg.id, msg);
        if (!request) return;
        shell.view = chainPan(shell.view, request, msg, frame, stage);
      },
    },
    location: { href: "https://app.test/" },
    addEventListener(type, listener) { listeners[type] = listener; },
    removeEventListener() {},
    getComputedStyle: () => (nested
      ? { overflowX: "scroll", overflowY: "scroll" }
      : { overflowX: "visible", overflowY: "visible" }),
    scrollX: 0,
    scrollY: 0,
    scrollBy(options) {
      window.scrollX = Math.max(0, window.scrollX + (options.left || 0));
      window.scrollY = Math.max(0, window.scrollY + (options.top || 0));
    },
  };
  vm.runInNewContext(source, { window, document, MouseEvent: class {} });

  let targeted = false;
  const send = (data) => listeners.message({ source: window.parent, origin: "https://shell.test", data });
  return {
    shell,
    window,
    // drag — one packet of finger travel in STAGE px, as the overlay measures it.
    drag(fx, fy, { deliver = true } = {}) {
      const scale = frame.base * shell.view.zoom;
      const packet = { type: "moa-scroll", x: 10, y: 10, dx: -fx / scale, dy: -fy / scale, reset: !targeted };
      if (shell.chainable) {
        packet.id = shell.chain.request({ dx: packet.dx, dy: packet.dy, scale });
        targeted = true;
      }
      if (!deliver) return packet;
      send(packet);
      return packet;
    },
    // deliver — a packet held back and handed over later, as a slow app would.
    deliver(packet) { send(packet); },
    endGesture() { targeted = false; },
  };
}

test("a page with room scrolls on the first zoomed drag and the frame stays put", () => {
  const root = scroller();
  const a = app({ target: root });
  a.window.scrollY = 900;

  a.drag(0, -300); // finger up 300 stage px at 2x → 150 app px down

  expect(a.window.scrollY).toBe(1050);
  expect(a.shell.view).toEqual({ zoom: 2, x: 0, y: -250 });
});

test("a nested list with room scrolls in both directions without panning the frame", () => {
  const list = scroller({ top: 400 });
  const a = app({ target: list, nested: true });

  a.drag(0, -200);
  expect(list.scrollTop).toBe(500);
  a.drag(0, 200);
  expect(list.scrollTop).toBe(400);
  expect(a.shell.view).toEqual({ zoom: 2, x: 0, y: -250 });
});

test("at the page top the drag reaches the magnified top and stops there", () => {
  const a = app({ target: scroller() }); // window.scrollY 0

  a.drag(0, 300); // 300 stage px down: 250 available, 50 nobody can take

  expect(a.window.scrollY).toBe(0);
  expect(a.shell.view).toEqual({ zoom: 2, x: 0, y: 0 });
});

test("one packet crossing the boundary splits between the app and the frame", () => {
  const a = app({ target: scroller() });
  a.window.scrollY = 100; // 100 app px available upward, then the frame

  a.drag(0, 300); // asks for 150 app px up

  expect(a.window.scrollY).toBe(0);
  expect(a.shell.view.y).toBeCloseTo(-150, 5); // 50 residual app px * scale 2
});

test("a diagonal packet can be taken by the app on one axis and the frame on the other", () => {
  const list = scroller({ top: 0, left: 0, width: 2000 });
  const a = app({ target: list, nested: true, view: { zoom: 2, x: -100, y: -250 } });

  a.drag(-60, 300); // horizontally the list has room; vertically it is at its top

  expect(list.scrollLeft).toBe(30);
  expect(list.scrollTop).toBe(0);
  expect(a.shell.view.x).toBe(-100);
  expect(a.shell.view.y).toBe(0);
});

test("a delayed answer still completes the gesture the finger just ended", () => {
  const a = app({ target: scroller() });
  const held = a.drag(0, 300, { deliver: false });
  a.endGesture();

  a.deliver(held);

  expect(a.shell.view).toEqual({ zoom: 2, x: 0, y: 0 });
});

test("an answer that outlived a pinch or a reset cannot move the new view", () => {
  const a = app({ target: scroller() });
  const held = a.drag(0, 300, { deliver: false });

  // The user pinched (or hit 1:1): the chain is cut and the view is another one.
  a.shell.chain.invalidate();
  a.shell.view = IDENTITY;
  a.deliver(held);

  expect(a.shell.view).toEqual(IDENTITY);
});

test("two packets in flight both move the frame — neither clamps from a stale pan", () => {
  const a = app({ target: scroller() });
  const first = a.drag(0, 100, { deliver: false });
  const second = a.drag(0, 100, { deliver: false });

  a.deliver(first);
  a.deliver(second);

  expect(a.shell.view.y).toBeCloseTo(-50, 5); // -250 + 100 + 100
});

test("an app that never announces the chain keeps the untagged relay and waits for nothing", () => {
  const root = scroller();
  const a = app({ target: root });
  a.shell.chainable = false; // an older inspector: no `chain: true`
  a.window.scrollY = 0;

  a.drag(0, 300);

  expect(a.shell.chain.pendingCount).toBe(0);
  expect(a.shell.view).toEqual({ zoom: 2, x: 0, y: -250 }); // no pan, as before
});
