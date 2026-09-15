import { describe, expect, it } from "bun:test";
import { readFileSync } from "node:fs";
import { claimDevice, authorizeDevice } from "./native-auth.js";

describe("native device authentication", () => {
  it("claims and authorizes through the native bridge without exposing a result", async () => {
    const calls = [];
    const bridge = {
      claim: async (...args) => { calls.push(["claim", ...args]); return { credential: "must-not-escape" }; },
      authorize: async (...args) => { calls.push(["authorize", ...args]); },
    };

    await expect(claimDevice("https://moa.example", "moa-pair-v1:id:secret", "moa app", bridge))
      .resolves.toBeUndefined();
    await expect(authorizeDevice("https://moa.example", bridge)).resolves.toBeUndefined();
    expect(calls).toEqual([
      ["claim", "https://moa.example", "moa-pair-v1:id:secret", "moa app"],
      ["authorize", "https://moa.example"],
    ]);
  });

  it("fails closed when the native bridge is absent", async () => {
    await expect(claimDevice("https://moa.example", "payload", "phone", {}))
      .rejects.toThrow("Native device authentication is unavailable");
  });

  it("keeps the durable credential in a device-only Keychain item", () => {
    const source = readFileSync(new URL("../ios/App/App/DeviceCredentialStore.swift", import.meta.url), "utf8");
    expect(source).toContain("SecItemAdd");
    expect(source).toContain("SecItemCopyMatching");
    expect(source).toContain("kSecAttrAccessibleWhenUnlockedThisDeviceOnly");
    expect(source).not.toContain("UserDefaults");
  });

  it("uses an ephemeral native session and refuses redirects", () => {
    const source = readFileSync(new URL("../ios/App/App/DeviceAuthBridge.swift", import.meta.url), "utf8");
    expect(source).toContain("URLSessionConfiguration.ephemeral");
    expect(source).toContain("completionHandler(nil)");
    expect(source).toContain('request.setValue("Moa-Device \\(stored.credential)", forHTTPHeaderField: "Authorization")');
  });
});
