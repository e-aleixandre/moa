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

  it("treats backing out of the scanner as a cancel, not a failed scan", async () => {
    const scanner = {
      isSupported: async () => ({ supported: true }),
      checkPermissions: async () => ({ camera: "granted" }),
      scan: async () => ({ barcodes: [] }),
    };
    await expect(scanPairingCode({
      Plugins: { [BARCODE_SCANNER_PLUGIN]: scanner },
      isPluginAvailable: () => true,
    })).resolves.toEqual({ status: "cancelled" });
  });

  it("treats ML Kit's cancel rejection as a cancel and other rejections as failures", async () => {
    const make = (message) => ({
      Plugins: { [BARCODE_SCANNER_PLUGIN]: {
        isSupported: async () => ({ supported: true }),
        checkPermissions: async () => ({ camera: "granted" }),
        scan: async () => { throw new Error(message); },
      } },
      isPluginAvailable: () => true,
    });
    await expect(scanPairingCode(make("scan canceled."))).resolves.toEqual({ status: "cancelled" });
    await expect(scanPairingCode(make("camera failed"))).rejects.toThrow("camera failed");
  });

  it("still reports a scan that returned a code without a value", async () => {
    const scanner = {
      isSupported: async () => ({ supported: true }),
      checkPermissions: async () => ({ camera: "granted" }),
      scan: async () => ({ barcodes: [{}] }),
    };
    await expect(scanPairingCode({
      Plugins: { [BARCODE_SCANNER_PLUGIN]: scanner },
      isPluginAvailable: () => true,
    })).resolves.toEqual({ status: "empty" });
  });

  it("returns to pairing silently on cancel and keeps the real errors", () => {
    const boot = readFileSync("www/boot.js", "utf8");
    const cancelBranch = boot.match(/result\.status === "cancelled"\) \{([^}]*)\}/)?.[1];
    expect(cancelBranch?.trim()).toBe('fail("");');
    expect(boot).toContain("No QR code was found.");
    expect(boot).toContain("Could not scan. Enter the code by hand instead.");
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
