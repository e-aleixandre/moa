import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";

const jsx = readFileSync(new URL("./GlobalSettings.jsx", import.meta.url), "utf8");

describe("Cache keep-alive in Settings", () => {
  test("reads and writes the one endpoint", () => {
    expect(jsx).toContain('api("GET", "/api/cache-keepalive")');
    expect(jsx).toContain('api("PATCH", "/api/cache-keepalive", wire)');
  });

  test("one switch per role, then what it spent and saved today; no limit", () => {
    const sec = jsx.slice(jsx.indexOf('class="zl-set-k">Cache keep-alive'), jsx.indexOf("Access sits after Subagents"));
    const labels = [...sec.matchAll(/<Row label="([^"]+)"/g)].map((m) => m[1]);
    expect(labels).toEqual(["Owners", "Sessions", "Subagents", "Spent today", "Saved today"]);
    expect([...sec.matchAll(/<Switch /g)].length).toBe(3);
    expect(sec).not.toContain("<input");
    expect(jsx).not.toContain("daily_budget_usd");
  });
});
