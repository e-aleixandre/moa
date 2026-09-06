import { test, expect } from "bun:test";
import {
  INSPECTOR_NOTICE,
  activatePreview,
  deactivatePreview,
  fetchPreviewStatus,
  portOf,
  setupStep,
  suggestPublicURL,
  validPublicURL,
} from "./preview-proxy.js";

// First run: the address the browser must use to reach the proxy is proposed
// from the address the user is already on, so confirming is usually enough.
test("the suggested address keeps the host the user reached moa through", () => {
  expect(suggestPublicURL({ protocol: "https:", hostname: "dev.taild072ac.ts.net" }, 7402))
    .toBe("https://dev.taild072ac.ts.net:7402");
  expect(suggestPublicURL({ protocol: "http:", hostname: "192.168.1.20" }, 8081))
    .toBe("http://192.168.1.20:8081");
});

test("an IPv6 host is bracketed so the suggestion is a usable URL", () => {
  const suggested = suggestPublicURL({ protocol: "http:", hostname: "fd00::1" }, 7402);
  expect(suggested).toBe("http://[fd00::1]:7402");
  expect(new URL(suggested).port).toBe("7402");
});

test("no suggestion is offered when there is nothing to derive it from", () => {
  expect(suggestPublicURL(null, 7402)).toBe("");
  expect(suggestPublicURL({ protocol: "https:", hostname: "" }, 7402)).toBe("");
  expect(suggestPublicURL({ protocol: "https:", hostname: "dev.test" }, 0)).toBe("");
});

// The user may edit the suggestion: the port they type is the port Moa binds,
// so a corrected address moves the listener instead of pointing at nothing.
test("the port comes from the edited address", () => {
  expect(portOf("https://dev.test:9000")).toBe(9000);
  expect(portOf("https://dev.test")).toBe(443);
  expect(portOf("http://dev.test")).toBe(80);
  expect(portOf("nonsense")).toBe(0);
});

test("only an absolute http(s) address is accepted", () => {
  expect(validPublicURL("https://dev.test:7402")).toBe(true);
  expect(validPublicURL("  http://dev.test:7402 ")).toBe(true);
  expect(validPublicURL("dev.test:7402")).toBe(false);
  expect(validPublicURL("ftp://dev.test:7402")).toBe(false);
  expect(validPublicURL("")).toBe(false);
});

// The address is asked for once. Once the server reports one, the panel goes
// straight to the app.
test("the address is asked for only until the server has one", () => {
  expect(setupStep({ status: { supported: true, public_url: "" }, savedURL: "" })).toBe("url");
  expect(setupStep({ status: { supported: true, public_url: "" }, savedURL: "http://localhost:5173" })).toBe("address");
  expect(setupStep({ status: { supported: true, public_url: "https://dev.test:7402" }, savedURL: "http://localhost:5173" })).toBe(null);
  expect(setupStep({ status: { supported: true, public_url: "https://dev.test:7402" }, savedURL: "http://localhost:5173", editing: "address" })).toBe("address");
});

test("activation sends the app URL and the address in one owner request", async () => {
  let seen;
  const result = await activatePreview(async (path, options) => {
    seen = { path, options };
    return { ok: true, json: async () => ({ enabled: true, preview_url: "https://dev.test:7402/?preview_token=x" }) };
  }, { url: "http://localhost:5173", publicURL: "https://dev.test:7402", port: 7402, parentOrigin: "https://dev.test:7401" });

  expect(result.preview_url).toBe("https://dev.test:7402/?preview_token=x");
  expect(seen.path).toBe("/api/preview/target");
  expect(seen.options.method).toBe("PUT");
  expect(seen.options.headers["X-Moa-Request"]).toBe("1");
  expect(JSON.parse(seen.options.body)).toEqual({
    url: "http://localhost:5173",
    parent_origin: "https://dev.test:7401",
    public_url: "https://dev.test:7402",
    port: 7402,
  });
});

// A busy port or an unusable address must arrive as the server's own words, so
// the user can act on it instead of reading "something went wrong".
test("a refused activation surfaces the server's message", async () => {
  const failing = async () => ({ ok: false, text: async () => "port 7402 is not available for the preview proxy" });
  await expect(activatePreview(failing, { url: "http://localhost:5173" }))
    .rejects.toThrow("port 7402 is not available for the preview proxy");
});

test("an empty error body still yields an actionable message", async () => {
  const failing = async () => ({ ok: false, text: async () => "" });
  await expect(activatePreview(failing, { url: "http://localhost:5173" }))
    .rejects.toThrow("The preview proxy could not be started.");
});

// Turning the preview off must take the port down even as the page unloads.
test("deactivation is a keepalive request that carries no target", async () => {
  let seen;
  await deactivatePreview(async (path, options) => {
    seen = { path, options };
    return { ok: true };
  });
  expect(seen.path).toBe("/api/preview/target");
  expect(seen.options.keepalive).toBe(true);
  expect(JSON.parse(seen.options.body)).toEqual({ enabled: false });
});

test("status is read uncached so a stale listener is never assumed", async () => {
  let seen;
  const status = await fetchPreviewStatus(async (path, options) => {
    seen = { path, options };
    return { ok: true, json: async () => ({ enabled: false, supported: true, suggested_port: 7402 }) };
  });
  expect(status.suggested_port).toBe(7402);
  expect(seen.options.cache).toBe("no-store");
});

// The notice used to describe the mechanism ("add the inspector script"). It
// has to tell the user what to do instead.
test("the inspector notice instructs an action, not the internal model", () => {
  expect(INSPECTOR_NOTICE).toContain("Reload the preview");
  expect(INSPECTOR_NOTICE).not.toContain("inspector script");
  expect(INSPECTOR_NOTICE).not.toContain("did not connect");
});
