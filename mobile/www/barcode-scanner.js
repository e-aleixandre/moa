// The package registers this exact native plugin name when its JavaScript
// entry point is bundled. The local pairing page has no bundler, so it must
// create the same Capacitor proxy itself before looking in Plugins.
export const BARCODE_SCANNER_PLUGIN = "BarcodeScanner";

export function getBarcodeScanner(capacitor = globalThis.Capacitor) {
  if (!capacitor?.isPluginAvailable?.(BARCODE_SCANNER_PLUGIN)) return null;

  const registered = capacitor.Plugins?.[BARCODE_SCANNER_PLUGIN];
  if (registered) return registered;
  if (typeof capacitor.registerPlugin !== "function") return null;
  return capacitor.registerPlugin(BARCODE_SCANNER_PLUGIN);
}

// Sideloaded Android builds do not get the Google code scanner module at
// install time (only Play installs honour the manifest dependency), so it is
// requested on first scan and awaited.
const MODULE_COMPLETED = 4;
const MODULE_FAILED = 5;
const MODULE_CANCELED = 3;

export async function ensureGoogleScannerModule(scanner, capacitor, timeoutMs = 60000) {
  if (capacitor?.getPlatform?.() !== "android") return true;
  if (typeof scanner.isGoogleBarcodeScannerModuleAvailable !== "function") return true;
  const { available } = await scanner.isGoogleBarcodeScannerModuleAvailable();
  if (available) return true;

  let finish;
  const done = new Promise((resolve) => { finish = resolve; });
  const timer = setTimeout(() => finish(false), timeoutMs);
  let handle;
  try {
    handle = await scanner.addListener("googleBarcodeScannerModuleInstallProgress", (event) => {
      if (event?.state === MODULE_COMPLETED) finish(true);
      else if (event?.state === MODULE_FAILED || event?.state === MODULE_CANCELED) finish(false);
    });
    await scanner.installGoogleBarcodeScannerModule();
    return await done;
  } catch {
    return false;
  } finally {
    clearTimeout(timer);
    handle?.remove?.();
  }
}

export async function scanPairingCode(capacitor = globalThis.Capacitor) {
  const scanner = getBarcodeScanner(capacitor);
  if (!scanner) return { status: "unavailable" };

  const support = await scanner.isSupported();
  if (!support?.supported) return { status: "unsupported" };
  if (!(await ensureGoogleScannerModule(scanner, capacitor))) return { status: "unavailable" };

  let permission = await scanner.checkPermissions();
  if (permission?.camera !== "granted") {
    permission = await scanner.requestPermissions();
  }
  if (permission?.camera !== "granted") return { status: "denied" };

  const result = await scanner.scan({ formats: ["QR_CODE"] });
  const barcode = result?.barcodes?.[0];
  const value = barcode?.rawValue || barcode?.displayValue;
  return value ? { status: "scanned", value } : { status: "empty" };
}
