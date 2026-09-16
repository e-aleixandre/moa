import { expect, test } from "bun:test";

const appPlist = await Bun.file("ios/App/App/Info.plist").text();
const voiceCapture = await Bun.file("../pkg/serve/frontend/src/data/voice-capture.js").text();
const barcodeScanner = await Bun.file("www/barcode-scanner.js").text();

function usageDescription(key) {
  const match = appPlist.match(new RegExp(`<key>${key}</key>\\s*<string>([^<]+)</string>`));
  return match?.[1] || "";
}

test("iOS declares microphone use when the frontend records audio", () => {
  expect(voiceCapture).toContain("getUserMedia({ audio: true })");
  expect(usageDescription("NSMicrophoneUsageDescription")).toBe("Record a voice message for moa.");
});

test("iOS declares camera use when the pairing scanner requests it", () => {
  expect(barcodeScanner).toContain("scanner.requestPermissions()");
  expect(usageDescription("NSCameraUsageDescription")).toBe("Scan a moa pairing code.");
});
