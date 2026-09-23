import { test, expect } from "bun:test";
import {
  INSPECTOR_NOTICE,
  activatePreview,
  checkPreviewReachable,
  deactivatePreview,
  fetchPreviewStatus,
  suggestPublicURL,
} from "./preview-proxy.js";

test("each device's preview address keeps the host it reached moa through", () => {
  expect(suggestPublicURL({ protocol: "https:", hostname: "dev.taild072ac.ts.net" }, 7402))
    .toBe("https://dev.taild072ac.ts.net:7402");
  expect(suggestPublicURL({ protocol: "http:", hostname: "192.168.1.20" }, 8081))
    .toBe("http://192.168.1.20:8081");
  expect(suggestPublicURL({ protocol: "https:", hostname: "dev.taild072ac.ts.net" }, 7351))
    .toBe("https://dev.taild072ac.ts.net:7351");
});

test("an IPv6 host is bracketed so the suggestion is a usable URL", () => {
  const suggested = suggestPublicURL({ protocol: "http:", hostname: "fd00::1" }, 7402);
  expect(suggested).toBe("http://[fd00::1]:7402");
  expect(new URL(suggested).port).toBe("7402");
});

test("no address is derived when there is nothing to derive it from", () => {
  expect(suggestPublicURL(null, 7402)).toBe("");
  expect(suggestPublicURL({ protocol: "https:", hostname: "" }, 7402)).toBe("");
  expect(suggestPublicURL({ protocol: "https:", hostname: "dev.test" }, 0)).toBe("");
});

test("activation sends the app URL and the current device address", async () => {
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

test("an unauthenticated opaque response establishes that the preview port is reachable", async () => {
  let request;
  await checkPreviewReachable(async (url, options) => {
    request = { url, options };
    return { type: "opaque", status: 0 };
  }, "https://dev.test:7351");
  expect(request.url).toBe("https://dev.test:7351");
  expect(request.options.mode).toBe("no-cors");
  expect(request.options.credentials).toBe("omit");
  await expect(checkPreviewReachable(async () => { throw new TypeError("network error"); }, "https://dev.test:7351"))
    .rejects.toThrow("network error");
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
