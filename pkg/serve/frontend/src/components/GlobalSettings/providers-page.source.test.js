import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { SETTINGS_PAGES } from "./settings-rows.js";

// How Providers is reached. Source facts: a DOM test would need the whole
// sheet and both shells mounted to say what a grep says exactly.
const read = (p) => readFileSync(new URL(p, import.meta.url), "utf8");
const sheet = read("./GlobalSettings.jsx");
const desktop = read("../../layout/DesktopShell/DesktopShell.jsx");
const mobile = read("../../layout/mobile/MobileConversationScreen/MobileConversationScreen.jsx");

describe("Settings → Providers", () => {
  test("it is a pushed page of the sheet, from a row that is never disabled", () => {
    expect(SETTINGS_PAGES.providers).toBe("Providers");
    expect(sheet).toMatch(/onOpen=\{\(\) => setPage\("providers"\)\}/);
    expect(sheet).toMatch(/page === "providers" && \(\s*<ProvidersPage/);
    const row = sheet.slice(sheet.indexOf('label="Providers"'), sheet.indexOf('label="Devices"'));
    expect(row).not.toMatch(/loading=/);
    expect(row).toMatch(/attention=\{providerStatus\.attentionCount\}/);
  });

  test("both hosts open it on request, focused on a provider", () => {
    for (const host of [desktop, mobile]) {
      expect(host).toMatch(/subscribeProviderSettings\(/);
      expect(host).toMatch(/initialPage=\{providerFocus \? "providers" : "root"\}/);
      expect(host).toMatch(/providerFocus=\{providerFocus\}/);
    }
  });

  test("on the phone the drawer closes first: never two sheets", () => {
    const block = mobile.slice(mobile.indexOf("subscribeProviderSettings("), mobile.indexOf("subscribeProviderSettings(") + 400);
    expect(block).toMatch(/drawerOpen\)\s*\{\s*settingsPendingRef\.current = true;\s*closeDrawer\(\);/);
  });

  test("the badge refreshes on load, on foreground and on the roster tick", () => {
    const app = read("../../app.jsx");
    expect(app.match(/loadProviderStatus\(\)/g).length).toBeGreaterThanOrEqual(2);
    const actions = read("../../data/session-actions.js");
    const tick = actions.slice(actions.indexOf("export function startPolling"), actions.indexOf("export function stopPolling"));
    expect(tick).toMatch(/loadProviderStatus\(\)/);
  });
});
