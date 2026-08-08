import { afterEach, beforeEach, expect, test } from "bun:test";
import {
  forgetPendingAttention,
  rememberPendingAttention,
  rememberedPendingAttention,
} from "./attention-receipt-store.js";

const originalStorage = globalThis.localStorage;
let values;

function installStorage() {
  values = new Map();
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: {
      get length() { return values.size; },
      key(index) { return [...values.keys()][index] || null; },
      getItem(key) { return values.get(key) || null; },
      setItem(key, value) { values.set(key, value); },
      removeItem(key) { values.delete(key); },
    },
  });
}

beforeEach(installStorage);
afterEach(() => {
  if (originalStorage === undefined) delete globalThis.localStorage;
  else Object.defineProperty(globalThis, "localStorage", { configurable: true, value: originalStorage });
});

function pending(n, extra = {}) {
  return { id: `request-${n}`, unseenGen: n + 1, serverInstance: "server-a", ...extra };
}

test("the receipt cap never evicts a potentially live receipt", () => {
  for (let i = 0; i < 100; i++) {
    expect(rememberPendingAttention(`live-${i}`, pending(i))).toBe(true);
  }
  expect(rememberPendingAttention("new-live", pending(101))).toBe(false);
  expect(rememberedPendingAttention("live-0")).toMatchObject({ id: "request-0" });
  expect(rememberedPendingAttention("new-live")).toMatchObject({ id: "request-101" });
});

test("the receipt cap prefers dropping confirmed records before refusing a live receipt", () => {
  for (let i = 0; i < 99; i++) {
    expect(rememberPendingAttention(`live-${i}`, pending(i))).toBe(true);
  }
  const confirmed = { ...pending(100), confirmed: true, seenAt: Date.now() };
  values.set("moa-pending-attention:confirmed", JSON.stringify(confirmed));
  expect(rememberPendingAttention("new-live", pending(101))).toBe(true);
  expect(rememberedPendingAttention("confirmed")).toBeNull();
  expect(rememberedPendingAttention("new-live")).toMatchObject({ id: "request-101" });
});

test("forget removes a deleted session receipt", () => {
  expect(rememberPendingAttention("gone", pending(1))).toBe(true);
  forgetPendingAttention("gone");
  expect(rememberedPendingAttention("gone")).toBeNull();
});
