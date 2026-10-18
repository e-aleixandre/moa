import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";

import { keepAliveLine } from "./keepalive-line.js";

const src = readFileSync(new URL("./UsagePage.jsx", import.meta.url), "utf8");

describe("keep-alive in Usage", () => {
  test("a session with pings shows them apart, right under the spend", () => {
    const spend = src.indexOf('<span class="zl-kv-k">Spend</span>');
    const row = src.indexOf('<span class="zl-kv-k">Keep-alive</span>');
    const ctx = src.indexOf('<span class="zl-kv-k">Context</span>');
    expect(spend).toBeGreaterThan(0);
    expect(row).toBeGreaterThan(spend);
    expect(row).toBeLessThan(ctx);
  });

  test("no row before the first ping; cost, count and saving after", () => {
    expect(keepAliveLine({})).toBeNull();
    expect(keepAliveLine({ keepAlivePings: 0, keepAliveCostUSD: 0.5 })).toBeNull();
    const one = keepAliveLine({ keepAlivePings: 1, keepAliveCostUSD: 0.05 });
    expect(one.detail).toBe("1 ping");
    expect(one.note).toBe("");
    const three = keepAliveLine({ keepAlivePings: 3, keepAliveCostUSD: 0.15, keepAliveSavedUSD: 2.4 });
    expect(three.detail).toBe("3 pings");
    expect(three.value).toContain("0.15");
    expect(three.note).toContain("2.40");
  });
});

describe("keep-alive totals over the socket", () => {
  test("the keepalive event updates the session fields Usage reads", async () => {
    const api = readFileSync(new URL("../../data/api.js", import.meta.url), "utf8");
    expect(api).toContain("case 'keepalive':");
    const { handleWsKeepAlive } = await import("../../data/ws/session.js");
    const { store, setState } = await import("../../data/store.js");
    setState({ sessions: { s1: { id: "s1" } } });
    handleWsKeepAlive("s1", { pings: 3, cost_usd: 0.15, saved_usd: 2 });
    const s = store.get().sessions.s1;
    expect([s.keepAlivePings, s.keepAliveCostUSD, s.keepAliveSavedUSD]).toEqual([3, 0.15, 2]);
  });
});
