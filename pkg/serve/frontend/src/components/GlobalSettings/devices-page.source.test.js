import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { SETTINGS_PAGES } from "./settings-rows.js";

// The Devices page reached the product as a MOCKUP the owner chose from, and
// what is easy to lose on the way in is not the look but the two decisions
// underneath it: that it is reached the way every other second level is, and
// that it draws nothing the server does not send.
//
// Those are source facts, so they are read from the source. A DOM test would
// need the whole sheet mounted to say something a grep says exactly.

const jsx = readFileSync(new URL("./GlobalSettings.jsx", import.meta.url), "utf8");
const css = readFileSync(new URL("./GlobalSettings.css", import.meta.url), "utf8");

describe("how Devices is reached", () => {
  test("it is a page of this sheet, not a surface of its own", () => {
    expect(SETTINGS_PAGES.devices).toBe("Devices");
    // The same mechanism as Before compacting / Summarize with / Subagent
    // models: a row calls setPage and the head swaps to back + title.
    expect(jsx).toMatch(/onOpen=\{\(\) => setPage\("devices"\)\}/);
    expect(jsx).toMatch(/page === "devices" && <DevicesPage/);
  });

  test("the row sits in an Access section, after Subagents and before About", () => {
    const keys = [...jsx.matchAll(/class="zl-set-k">([^<]+)</g)].map((m) => m[1]);
    expect(keys).toEqual(["Context", "Notifications", "Subagents", "Access", "About"]);
  });

  test("the row states its count on the right, like every other row's value", () => {
    expect(jsx).toMatch(/value=\{devicesValue\(devices\.devices, devices\.loaded, !!devices\.failure\)\}/);
    expect(jsx).toMatch(/loading=\{!devices\.loaded\}/);
  });
});

describe("what the page may draw", () => {
  // devicePublic (pkg/serve/device_auth.go:146) sends exactly these. A field
  // that is not one of them is either invented or a backend change nobody
  // told this screen about; both deserve to fail here.
  const WIRE = ["id", "label", "issued_at", "expires_at", "revoked_at", "last_used_at"];

  test("it reads no field the wire does not carry", () => {
    const page = jsx.slice(jsx.indexOf("function DeviceRow"), jsx.indexOf("// ── The sheet"));
    const read = [...page.matchAll(/device\.([a-z_]+)/g)].map((m) => m[1]);
    expect([...new Set(read)].sort()).toEqual(WIRE.filter((f) => read.includes(f)).sort());
    for (const field of read) expect(WIRE).toContain(field);
  });

  test("nothing claims to be the device in your hand — the server sends no such flag", () => {
    expect(jsx).not.toMatch(/is_current|this_device|isCurrentDevice/);
  });
});

describe("the page's motion and colour", () => {
  test("a revoked row fades in place; the list is not re-sorted under the finger", () => {
    // markRevoked preserves order (devices-model.test.js); this is the other
    // half: the row transitions rather than being removed and reflowing.
    expect(css).toMatch(/\.zl-set-dev\.is-gone \{ opacity/);
    expect(css).toMatch(/transition:\s*\n\s*opacity var\(--motion-fast\)/);
  });

  test("every animation it adds is answered under prefers-reduced-motion", () => {
    const guard = css.slice(css.lastIndexOf("@media (prefers-reduced-motion: reduce)"));
    expect(guard).toMatch(/\.zl-set-dev-ask \{ animation: none; \}/);
    expect(guard).toMatch(/\.zl-set-dev,[\s\S]*transition: none;/);
  });

  test("yellow means expiring and red is spent only on the destructive action", () => {
    expect(css).toMatch(/\.zl-set-dev-left\.is-soon \{ color: var\(--zl-yellow\); \}/);
    // The revoke button and its armed row are the only red things here.
    const reds = [...css.matchAll(/\.(zl-set-dev[a-z-]*)[^{]*\{[^}]*--zl-red|--red-dim/g)];
    expect(reds.length).toBeGreaterThan(0);
    expect(css).not.toMatch(/\.zl-set-dev-n[^{]*\{[^}]*--zl-red/);
  });

  test("it uses the canonical tokens, never a var() with a fallback of its own", () => {
    const own = css.slice(css.indexOf("── The devices page"));
    expect(own).not.toMatch(/var\(--zl-[a-z0-9-]+,/);
  });
});
