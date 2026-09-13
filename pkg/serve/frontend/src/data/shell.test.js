import { describe, it, expect } from "bun:test";
import {
  detectShell, ownsScreen, applyShell,
  SHELL_BROWSER, SHELL_PWA, SHELL_NATIVE,
} from "./shell.js";

// A window stub: only the parts detectShell reads.
function win({ capacitor = null, standalone = false, displayMode = false } = {}) {
  return {
    Capacitor: capacitor,
    navigator: { standalone },
    matchMedia: (q) => ({ matches: displayMode && q.includes("standalone") }),
  };
}

describe("detectShell", () => {
  it("is a browser tab by default", () => {
    expect(detectShell(win())).toBe(SHELL_BROWSER);
  });

  it("recognises an installed PWA by navigator.standalone", () => {
    expect(detectShell(win({ standalone: true }))).toBe(SHELL_PWA);
  });

  it("recognises an installed PWA by display-mode", () => {
    expect(detectShell(win({ displayMode: true }))).toBe(SHELL_PWA);
  });

  it("recognises a native container", () => {
    const cap = { isNativePlatform: () => true };
    expect(detectShell(win({ capacitor: cap }))).toBe(SHELL_NATIVE);
  });

  // The container loads moa from the server, and Capacitor injects its runtime
  // only into pages it serves itself: a remote page inside a perfectly good
  // container sees no window.Capacitor at all. Detecting the bridge the web
  // view was built with is how Capacitor identifies the platform itself
  // (@capacitor/core getPlatformId); without this the app would silently fall
  // back to the web layout inside the native container.
  it("recognises the container from the bridge alone, with no runtime", () => {
    const w = win();
    w.webkit = { messageHandlers: { bridge: {} } };
    expect(detectShell(w)).toBe(SHELL_NATIVE);
  });

  it("recognises the android bridge the same way", () => {
    const w = win();
    w.androidBridge = {};
    expect(detectShell(w)).toBe(SHELL_NATIVE);
  });

  // Safari exposes window.webkit of its own accord; only the bridge means a
  // container, or every iPhone would claim the whole screen and lose 62px.
  it("is not fooled by webkit without a bridge", () => {
    const w = win();
    w.webkit = { messageHandlers: {} };
    expect(detectShell(w)).toBe(SHELL_BROWSER);
  });

  it("takes Capacitor on the web for what it is: not native", () => {
    const cap = { isNativePlatform: () => false };
    expect(detectShell(win({ capacitor: cap }))).toBe(SHELL_BROWSER);
  });

  it("survives a bridge that throws", () => {
    const cap = { isNativePlatform: () => { throw new Error("no bridge"); } };
    expect(detectShell(win({ capacitor: cap, standalone: true }))).toBe(SHELL_PWA);
  });

  it("does not need a window", () => {
    expect(detectShell(null)).toBe(SHELL_BROWSER);
  });
});

describe("ownsScreen", () => {
  // The inversion that matters. An installed iOS PWA reports standalone,
  // resolves safe-area insets and paints under the status bar, yet receives a
  // window 62px shorter than the screen (measured: 402x812 on a 402x874
  // device, WebKit 313800). If this ever returns true for a PWA, the composer
  // is pushed up and a dead strip appears below it.
  it("refuses the whole screen to an installed PWA", () => {
    expect(ownsScreen(SHELL_PWA)).toBe(false);
  });

  it("refuses it to a browser tab", () => {
    expect(ownsScreen(SHELL_BROWSER)).toBe(false);
  });

  it("grants it only to a native container", () => {
    expect(ownsScreen(SHELL_NATIVE)).toBe(true);
  });
});

describe("applyShell", () => {
  function doc() {
    const set = new Set();
    return {
      documentElement: {
        dataset: {},
        classList: {
          toggle: (c, on) => (on ? set.add(c) : set.delete(c)),
          contains: (c) => set.has(c),
        },
      },
    };
  }

  it("marks the document only when the screen is really owned", () => {
    const d = doc();
    applyShell(SHELL_NATIVE, d);
    expect(d.documentElement.classList.contains("shell-edge")).toBe(true);
    expect(d.documentElement.dataset.shell).toBe(SHELL_NATIVE);

    applyShell(SHELL_PWA, d);
    expect(d.documentElement.classList.contains("shell-edge")).toBe(false);
    expect(d.documentElement.dataset.shell).toBe(SHELL_PWA);
  });
});
