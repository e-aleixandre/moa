import { describe, it, expect, beforeEach, afterEach } from "bun:test";
import { tap, hasHaptics, resetForTest } from "./haptics.js";

const realNavigator = globalThis.navigator;

function withBridge(plugin) {
  globalThis.Capacitor = { Plugins: { Haptics: plugin } };
  resetForTest();
}

function withoutBridge() {
  delete globalThis.Capacitor;
  resetForTest();
}

function stubNavigator(vibrate) {
  Object.defineProperty(globalThis, "navigator", {
    value: vibrate ? { vibrate } : {},
    configurable: true,
  });
}

beforeEach(() => { withoutBridge(); });

afterEach(() => {
  withoutBridge();
  Object.defineProperty(globalThis, "navigator", {
    value: realNavigator,
    configurable: true,
  });
});

describe("tap on the web", () => {
  it("uses navigator.vibrate when there is one", () => {
    const calls = [];
    stubNavigator((p) => calls.push(p));
    expect(tap("select")).toBe(true);
    expect(calls).toEqual([12]);
  });

  // iOS: no vibrate, no bridge. The call must be a no-op that reports it did
  // nothing, not an exception in the middle of a drag.
  it("reports doing nothing where the platform has nothing", () => {
    stubNavigator(null);
    expect(tap("select")).toBe(false);
  });

  it("survives a browser that throws without user activation", () => {
    stubNavigator(() => { throw new Error("not allowed"); });
    expect(tap("impact")).toBe(false);
  });
});

describe("tap in the container", () => {
  it("asks for a selection tick, not a vibration", () => {
    const calls = [];
    const vibrated = [];
    stubNavigator((p) => vibrated.push(p));
    withBridge({
      selectionStart: () => calls.push("start"),
      selectionChanged: () => calls.push("selection"),
      selectionEnd: () => calls.push("end"),
      impact: (o) => calls.push(`impact:${o.style}`),
      notification: (o) => calls.push(`notify:${o.type}`),
    });

    expect(tap("select")).toBe(true);
    expect(calls).toEqual(["start", "selection", "end"]);
    // The web pattern must not also fire: two engines answering one intent is
    // a double tick on Android.
    expect(vibrated).toEqual([]);
  });

  it("maps impact and notify to their native shapes", () => {
    const calls = [];
    stubNavigator(null);
    withBridge({
      selectionStart: () => calls.push("start"),
      selectionChanged: () => calls.push("selection"),
      selectionEnd: () => calls.push("end"),
      impact: (o) => calls.push(`impact:${o.style}`),
      notification: (o) => calls.push(`notify:${o.type}`),
    });

    tap("impact");
    tap("notify");
    expect(calls).toEqual(["impact:MEDIUM", "notify:SUCCESS"]);
  });

  it("starts the native selection generator before asking it to tick", () => {
    let started = false;
    let felt = false;
    stubNavigator(null);
    withBridge({
      selectionStart: () => { started = true; },
      selectionChanged: () => { if (started) felt = true; },
      selectionEnd: () => { started = false; },
    });

    expect(tap("select")).toBe(true);
    expect(felt).toBe(true);
    expect(started).toBe(false);
  });

  it("falls back to the web when the bridge throws", () => {
    const vibrated = [];
    stubNavigator((p) => vibrated.push(p));
    withBridge({
      selectionStart: () => {},
      selectionChanged: () => { throw new Error("no bridge"); },
      selectionEnd: () => {},
    });
    expect(tap("select")).toBe(true);
    expect(vibrated).toEqual([12]);
  });
});

describe("hasHaptics", () => {
  it("is true in the container", () => {
    stubNavigator(null);
    withBridge({ selectionChanged: () => {} });
    expect(hasHaptics()).toBe(true);
  });

  it("is false on an iPhone web app", () => {
    stubNavigator(null);
    expect(hasHaptics()).toBe(false);
  });
});
