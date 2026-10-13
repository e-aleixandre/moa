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

  it("gives the share extension the app's private keychain group, never the push group", () => {
    const share = readFileSync("ios/App/ShareExtension/ShareExtension.entitlements", "utf8");
    const shareInfo = readFileSync("ios/App/ShareExtension/Info.plist", "utf8");
    const nse = readFileSync("ios/App/NotificationService/NotificationService.entitlements", "utf8");
    const app = readFileSync("ios/App/App/App.entitlements", "utf8");
    const groups = (plist) => plist.match(/<key>keychain-access-groups<\/key>\s*<array>([\s\S]*?)<\/array>/)?.[1]
      .match(/<string>[^<]+<\/string>/g) ?? [];
    // PRODUCT_BUNDLE_IDENTIFIER inside the extension is the extension's own, so the
    // app's id comes from a project-level setting.
    expect(groups(share)).toEqual(["<string>$(AppIdentifierPrefix)$(MOA_APP_BUNDLE_IDENTIFIER)</string>"]);
    expect(share).not.toContain("MOA_PUSH_KEYCHAIN_GROUP");
    expect(shareInfo).not.toContain("MoaKeychainPushGroup");
    expect(shareInfo).toContain("<string>$(AppIdentifierPrefix)$(MOA_APP_BUNDLE_IDENTIFIER)</string>");
    // The extension must look up the app's service name, not its own bundle id.
    expect(shareInfo).toContain("<string>$(MOA_APP_BUNDLE_IDENTIFIER).device-auth</string>");
    // The notification extension still cannot see the device credential.
    expect(groups(nse)).toEqual(["<string>$(AppIdentifierPrefix)$(MOA_PUSH_KEYCHAIN_GROUP)</string>"]);
    expect(groups(app)).toContain("<string>$(AppIdentifierPrefix)$(PRODUCT_BUNDLE_IDENTIFIER)</string>");

    const project = readFileSync("ios/App/App.xcodeproj/project.pbxproj", "utf8");
    expect(project).toContain('PRODUCT_BUNDLE_IDENTIFIER = "$(MOA_APP_BUNDLE_IDENTIFIER)";');
  });

  it("only reads the device credential from the share extension", () => {
    const dir = "ios/App/ShareExtension/";
    const sources = ["ShareClient.swift", "ShareViewController.swift", "ShareLogic.swift", "ShareBodyWriter.swift"]
      .map((name) => readFileSync(dir + name, "utf8")).join("\n");
    expect(sources).toContain("store.load()");
    expect(sources).not.toMatch(/\.save\(|\.delete\(\)|SecItemAdd|SecItemDelete|SecItemUpdate/);
    expect(sources).toContain("completionHandler(nil)");
    expect(sources).toContain("URLSessionConfiguration.ephemeral");
  });
});
