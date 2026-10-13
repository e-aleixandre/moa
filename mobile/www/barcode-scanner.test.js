import { describe, expect, it } from "bun:test";
import { readFileSync } from "node:fs";
import {
  BARCODE_SCANNER_PLUGIN, getBarcodeScanner, scanPairingCode,
} from "./barcode-scanner.js";

describe("native barcode scanner contract", () => {
  it("registers the exact plugin name exported by the installed package", () => {
    const packageEntry = readFileSync(
      "node_modules/@capacitor-mlkit/barcode-scanning/dist/esm/index.js",
      "utf8",
    );
    const podfile = readFileSync("ios/App/Podfile", "utf8");
    const project = readFileSync("ios/App/App.xcodeproj/project.pbxproj", "utf8");
    const boot = readFileSync("www/boot.js", "utf8");
    const infoPlist = readFileSync("ios/App/App/Info.plist", "utf8");
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

  it("installs the Google scanner module on Android before scanning", async () => {
    const order = [];
    let listener;
    const scanner = {
      isSupported: async () => ({ supported: true }),
      isGoogleBarcodeScannerModuleAvailable: async () => ({ available: false }),
      addListener: async (name, fn) => { order.push(name); listener = fn; return { remove() { order.push("removed"); } }; },
      installGoogleBarcodeScannerModule: async () => { order.push("install"); setTimeout(() => listener({ state: 4 }), 0); },
      checkPermissions: async () => ({ camera: "granted" }),
      scan: async () => { order.push("scan"); return { barcodes: [{ rawValue: "x" }] }; },
    };
    const result = await scanPairingCode({
      Plugins: { [BARCODE_SCANNER_PLUGIN]: scanner },
      isPluginAvailable: () => true,
      getPlatform: () => "android",
    });
    expect(result).toEqual({ status: "scanned", value: "x" });
    expect(order).toEqual(["googleBarcodeScannerModuleInstallProgress", "install", "removed", "scan"]);
  });

  it("falls back to manual entry when the module install fails", async () => {
    let listener;
    const scanner = {
      isSupported: async () => ({ supported: true }),
      isGoogleBarcodeScannerModuleAvailable: async () => ({ available: false }),
      addListener: async (_name, fn) => { listener = fn; return { remove() {} }; },
      installGoogleBarcodeScannerModule: async () => { setTimeout(() => listener({ state: 5 }), 0); },
      scan: async () => { throw new Error("must not scan"); },
    };
    await expect(scanPairingCode({
      Plugins: { [BARCODE_SCANNER_PLUGIN]: scanner },
      isPluginAvailable: () => true,
      getPlatform: () => "android",
    })).resolves.toEqual({ status: "unavailable" });
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
