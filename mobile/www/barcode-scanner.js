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

const ML_KIT_CANCELED = "scan canceled.";

export async function scanPairingCode(capacitor = globalThis.Capacitor) {
  const scanner = getBarcodeScanner(capacitor);
  if (!scanner) return { status: "unavailable" };

  const support = await scanner.isSupported();
  if (!support?.supported) return { status: "unsupported" };

  let permission = await scanner.checkPermissions();
  if (permission?.camera !== "granted") {
    permission = await scanner.requestPermissions();
  }
  if (permission?.camera !== "granted") return { status: "denied" };

  let result;
  try {
    result = await scanner.scan({ formats: ["QR_CODE"] });
  } catch (err) {
    // ML Kit (iOS until it is replaced) rejects a cancel instead of resolving.
    if (err?.message === ML_KIT_CANCELED) return { status: "cancelled" };
    throw err;
  }
  // The native scanners resolve an empty list when the person backs out;
  // that is a choice, not a failure.
  if (Array.isArray(result?.barcodes) && result.barcodes.length === 0) return { status: "cancelled" };
  const barcode = result?.barcodes?.[0];
  const value = barcode?.rawValue || barcode?.displayValue;
  return value ? { status: "scanned", value } : { status: "empty" };
}
