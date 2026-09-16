import { describe, expect, it } from "bun:test";
import { readFileSync } from "node:fs";
import {
  BARCODE_SCANNER_PLUGIN, getBarcodeScanner, scanPairingCode,
} from "./barcode-scanner.js";

describe("native barcode scanner contract", () => {
  it("registers the exact plugin name exported by the installed package", () => {
    const packageEntry = readFileSync(new URL(
      "../node_modules/@capacitor-mlkit/barcode-scanning/dist/esm/index.js",
      import.meta.url,
    ), "utf8");
    const podfile = readFileSync(new URL("../ios/App/Podfile", import.meta.url), "utf8");
    const project = readFileSync(new URL(
      "../ios/App/App.xcodeproj/project.pbxproj",
      import.meta.url,
    ), "utf8");
    const boot = readFileSync(new URL("./boot.js", import.meta.url), "utf8");
    const infoPlist = readFileSync(new URL("../ios/App/App/Info.plist", import.meta.url), "utf8");
    const packagePluginName = packageEntry.match(/registerPlugin\((['"])([^'"]+)\1/)?.[2];

    expect(packagePluginName).toBe(BARCODE_SCANNER_PLUGIN);
    expect(boot).toContain('import { scanPairingCode } from "./barcode-scanner.js"');
    expect(podfile).toContain("pod 'CapacitorMlkitBarcodeScanning'");
    expect(project).toContain("Pods_App.framework in Frameworks");
    expect(project).not.toContain("CapApp-SPM");
    expect(infoPlist).toContain("<key>NSCameraUsageDescription</key>");

    const calls = [];
    const proxy = {};
    const scanner = getBarcodeScanner({
      Plugins: {},
      isPluginAvailable: (name) => name === packagePluginName,
      registerPlugin: (name) => { calls.push(name); return proxy; },
    });
    expect(scanner).toBe(proxy);
    expect(calls).toEqual([packagePluginName]);
  });

  it("uses the installed scan API and returns its barcode value", async () => {
    const calls = [];
    const scanner = {
      isSupported: async () => ({ supported: true }),
      checkPermissions: async () => ({ camera: "prompt" }),
      requestPermissions: async () => ({ camera: "granted" }),
      scan: async (options) => {
        calls.push(options);
        return { barcodes: [{ rawValue: "moa-link-v1:fixture" }] };
      },
    };
    const result = await scanPairingCode({
      Plugins: { [BARCODE_SCANNER_PLUGIN]: scanner },
      isPluginAvailable: () => true,
    });

    expect(result).toEqual({ status: "scanned", value: "moa-link-v1:fixture" });
    expect(calls).toEqual([{ formats: ["QR_CODE"] }]);
  });

  it("reports a real camera absence instead of treating it as registration failure", async () => {
    const scanner = {
      isSupported: async () => ({ supported: false }),
    };
    await expect(scanPairingCode({
      Plugins: { [BARCODE_SCANNER_PLUGIN]: scanner },
      isPluginAvailable: () => true,
    })).resolves.toEqual({ status: "unsupported" });
  });
});
