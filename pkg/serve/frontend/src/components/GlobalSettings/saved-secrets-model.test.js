import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { savedLine, savedValue, sortSaved, when, withoutAlias } from "./saved-secrets-model.js";
import { SETTINGS_PAGES } from "./settings-rows.js";
import { savedSecretsBridge, listSaved } from "../../data/saved-secrets.js";

const jsx = readFileSync(new URL("./GlobalSettings.jsx", import.meta.url), "utf8");

const NOW = new Date(2026, 9, 13, 18, 0).getTime();
const at = (d, h, m) => new Date(2026, 9, d, h, m).getTime();

describe("the saved passwords list", () => {
  test("a use today reads as a time, another day as a date", () => {
    expect(when(at(13, 14, 2), NOW)).toBe("14:02");
    expect(when(at(2, 9, 0), NOW)).toBe("2 Oct");
  });

  test("the line says when, then what it is for; never a value", () => {
    const used = { alias: "agents-keychain", actionSummary: "ssh … security unlock-keychain", savedAt: at(1, 9, 0), lastUsedAt: at(13, 14, 2) };
    expect(savedLine(used, NOW)).toBe("Last used 14:02 · ssh … security unlock-keychain");
    const fresh = { alias: "x", actionSummary: "idmsa.apple.com", savedAt: at(2, 10, 0), lastUsedAt: null };
    expect(savedLine(fresh, NOW)).toBe("Saved 2 Oct · idmsa.apple.com");
  });

  test("most recently used first, and forgetting one leaves the rest", () => {
    const list = [
      { alias: "a", savedAt: 1, lastUsedAt: null },
      { alias: "b", savedAt: 1, lastUsedAt: 9 },
      { alias: "c", savedAt: 5, lastUsedAt: null },
    ];
    expect(sortSaved(list).map((i) => i.alias)).toEqual(["b", "c", "a"]);
    expect(withoutAlias(list, "b").map((i) => i.alias)).toEqual(["a", "c"]);
  });

  test("the row's reading is a count, or None", () => {
    expect(savedValue(0, false)).toBeNull();
    expect(savedValue(0, true)).toBe("None");
    expect(savedValue(2, true)).toBe("2");
  });
});

describe("where it lives", () => {
  test("it is a page of the settings sheet, reached from Access, only with the native bridge", () => {
    expect(SETTINGS_PAGES["saved-secrets"]).toBe("Saved passwords");
    expect(jsx).toMatch(/page === "saved-secrets" && <SavedSecretsPage/);
    expect(jsx).toMatch(/\{saved\.available && \(\s*<Row\s+label="Saved passwords"/);
    expect(jsx).toContain("Passwords saved on this iPhone");
    expect(jsx).toMatch(/aria-label=\{`Forget \$\{item\.alias\}`\}/);
    expect(jsx).toContain("forgetAll");
  });

  test("no bridge on the web means no feature", () => {
    expect(savedSecretsBridge({})).toBeNull();
    expect(savedSecretsBridge({ moaSavedSecrets: {} })).toBeNull();
    expect(savedSecretsBridge({ moaSavedSecrets: { status() {} } })).not.toBeNull();
  });

  test("list and forget go through the bridge", async () => {
    const calls = [];
    const bridge = {
      status() {},
      list: async () => { calls.push("list"); return [{ alias: "a" }]; },
    };
    expect(await listSaved(bridge)).toEqual([{ alias: "a" }]);
    expect(await listSaved({ status() {}, list: async () => null })).toEqual([]);
    expect(calls).toEqual(["list"]);
  });
});
