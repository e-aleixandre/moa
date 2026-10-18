import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";

const jsx = readFileSync(new URL("./GlobalSettings.jsx", import.meta.url), "utf8");
const css = readFileSync(new URL("./GlobalSettings.css", import.meta.url), "utf8");

describe("Cache keep-alive in Settings", () => {
  test("reads and writes the one endpoint, with the wire names the server sends", () => {
    expect(jsx).toContain('api("GET", "/api/cache-keepalive")');
    expect(jsx).toContain('api("PATCH", "/api/cache-keepalive", wire)');
    expect(jsx).toContain('{ daily_budget_usd: usd }');
  });

  test("one switch per role, then the limit and what was spent", () => {
    const sec = jsx.slice(jsx.indexOf('class="zl-set-k">Cache keep-alive'), jsx.indexOf("Access sits after Subagents"));
    const labels = [...sec.matchAll(/<Row label="([^"]+)"/g)].map((m) => m[1]);
    expect(labels).toEqual(["Owners", "Sessions", "Subagents", "Daily limit", "Spent today"]);
    expect([...sec.matchAll(/<Switch /g)].length).toBe(3);
  });

  test("the limit field stays at the 16px iOS floor", () => {
    const rule = css.slice(css.indexOf(".zl-set-input input {"), css.indexOf(".zl-set-input input::placeholder"));
    expect(rule).toContain("font-size: var(--zl-fs-title)");
    expect(css).not.toMatch(/\.zl-set-input\.is-inline input[^}]*font-size/);
  });
});
