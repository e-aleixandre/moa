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
    const source = readFileSync("ios/App/App/DeviceCredentialStore.swift", "utf8");
    expect(source).toContain("SecItemAdd");
    expect(source).toContain("SecItemCopyMatching");
    expect(source).toContain("kSecAttrAccessibleWhenUnlockedThisDeviceOnly");
    expect(source).not.toContain("UserDefaults");
  });

  it("uses an ephemeral native session and refuses redirects", () => {
    const source = readFileSync("ios/App/App/DeviceAuthBridge.swift", "utf8");
    const controller = readFileSync("ios/App/App/MoaBridgeViewController.swift", "utf8");
    const config = JSON.parse(readFileSync("capacitor.config.json", "utf8"));
    const capacitorPolicy = readFileSync(
      "node_modules/@capacitor/ios/Capacitor/Capacitor/WebViewDelegationHandler.swift",
      "utf8",
    );
    expect(source).toContain("URLSessionConfiguration.ephemeral");
    expect(source).toContain("completionHandler(nil)");
    expect(source).toContain('request.setValue("Moa-Device \\(stored.credential)", forHTTPHeaderField: "Authorization")');

    // The paired origin cannot be put in Capacitor's build-time, host-only
    // allowNavigation list. Its plugin hook runs before Capacitor's Safari
    // fallback: false admits only the exact runtime origin, while nil leaves
    // every other top-level URL to the fallback unchanged.
    expect(config.server.allowNavigation).toBeUndefined();
    expect(controller).toContain("registerPluginInstance(PairedServerNavigationPlugin())");
    expect(controller).toContain("NativeServerBinding.matches(url)");
    expect(controller).toContain("return NSNumber(value: false)");
    expect(source).toContain('components.scheme?.lowercased() == "https"');
    expect(source).toContain("bound.host?.lowercased() == url.host?.lowercased()");
    expect(source).toContain("effectivePort(bound) == effectivePort(url)");
    expect(capacitorPolicy.indexOf("plugin.shouldOverrideLoad(navigationAction)"))
      .toBeLessThan(capacitorPolicy.indexOf("UIApplication.shared.open(navURL"));
  });

  it("keeps unpairing native, short-lived, and credential-private", () => {
    const auth = readFileSync("ios/App/App/DeviceAuthBridge.swift", "utf8");
    const scene = readFileSync("ios/App/App/SceneDelegate.swift", "utf8");
    const controller = readFileSync("ios/App/App/MoaBridgeViewController.swift", "utf8");
    const plist = readFileSync("ios/App/App/Info.plist", "utf8");

    expect(plist).toContain("UIApplicationShortcutItems");
    expect(scene).toContain("performActionFor shortcutItem");
    expect(controller).toContain('title: "Unpair moa?"');
    expect(auth).toContain('appendingPathComponent("api/pulse/device/revoke")');
    expect(auth).toContain("timeoutInterval: 3");
    expect(auth).toContain("configuration.timeoutIntervalForResource = 3");
    expect(auth).toContain("try? await reset()");
    expect(auth).not.toContain("print(");
  });
});
