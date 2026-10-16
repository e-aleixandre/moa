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

const pairingPage = await Bun.file("www/index.html").text();
const appManifest = await Bun.file("ios/App/App/PrivacyInfo.xcprivacy").text();
const pbxproj = await Bun.file("ios/App/App.xcodeproj/project.pbxproj").text();
const podfile = await Bun.file("ios/App/Podfile").text();
const packageJSON = await Bun.file("package.json").text();

test("the pairing screen links the privacy policy without leaving the app page", () => {
  expect(pairingPage).toMatch(/<a href="https:\/\/letmoa\.run\/privacy" target="_blank"/);
});

test("the build declares its privacy posture", () => {
  expect(appPlist).toMatch(/<key>ITSAppUsesNonExemptEncryption<\/key>\s*<false\/>/);
  // Background modes are only those voice calls use (audio while locked,
  // voip for CallKit); App Review rejects modes the app does not use.
  expect(appPlist).toMatch(/<key>UIBackgroundModes<\/key>\s*<array>\s*<string>audio<\/string>\s*<string>voip<\/string>\s*<\/array>/);
  expect(appManifest).toMatch(/<key>NSPrivacyTracking<\/key>\s*<false\/>/);
  expect(appManifest).toMatch(/<key>NSPrivacyCollectedDataTypes<\/key>\s*<array\/>/);
  expect(appManifest).toContain("<string>CA92.1</string>");
  expect(pbxproj).toContain("PrivacyInfo.xcprivacy in Resources");
});

test("no third-party analytics SDK ships in the container", () => {
  expect(`${podfile}\n${packageJSON}`.toLowerCase()).not.toMatch(/mlkit|firebase|google/);
});
