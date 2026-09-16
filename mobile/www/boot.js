// Binding the app to a moa, then getting out of the way.
//
// This runs only in the container's local page. Once a server is known the
// window is sent there and the real frontend takes over -- including the next
// launch, which never sees this screen again.

import {
  parsePairing, storedServer, rememberServer, forgetServer,
} from "./pairing.js";
import { claimDevice, authorizeDevice } from "./native-auth.js";
import { scanPairingCode } from "./barcode-scanner.js";

const $ = (id) => document.getElementById(id);
const error = $("error");

function fail(message) {
  error.textContent = message;
}

// The share inbox lives outside the web view in an App Group. Its bridge is
// injected by the iOS container into both this local page and the remote moa
// page. Binding the server here keeps arbitrary pages loaded in the web view
// from reading files that were shared with moa.
async function bindNativeServer(origin) {
  const inbox = globalThis.MoaShareInbox;
  if (typeof inbox?.bindServer === "function") await inbox.bindServer(origin);
}

async function clearNativeServer() {
  const inbox = globalThis.MoaShareInbox;
  if (typeof inbox?.clearServer === "function") await inbox.clearServer();
}

async function showPendingShare() {
  const inbox = globalThis.MoaShareInbox;
  if (typeof inbox?.status !== "function") return;
  try {
    const result = await inbox.status();
    $("pending-share").classList.toggle("on", Number(result?.pending) > 0);
  } catch {
    // Pairing remains usable when the native inbox is unavailable.
  }
}

// The one-time claim and durable credential stay in native code. The local
// page receives only success or a classified error and persists only the
// non-secret server origin.
async function claim(origin, payload) {
  await claimDevice(origin, payload, deviceLabel());
  rememberServer(origin);
}

// A label the owner can recognise in the device list, so revoking the right
// one does not require guessing which credential is which.
function deviceLabel() {
  const model = /iPad/.test(navigator.userAgent) ? "iPad" : "iPhone";
  return `moa app (${model})`;
}

async function pair(text) {
  const parsed = parsePairing(text);
  if (!parsed) {
    fail("That does not look like a moa pairing code.");
    return;
  }

  fail("");
  try {
    await claim(parsed.origin, parsed.payload);
  } catch (claimError) {
    if (claimError?.code === "keychain") {
      fail("Could not secure this device. Check its passcode settings and try again.");
    } else if (claimError?.code === "unavailable") {
      fail("Could not reach this moa. Check the connection and try again.");
    } else if (claimError?.code === "invalid") {
      fail("That does not look like a moa pairing code.");
    } else {
      fail("This pairing code has expired or was already used. Create a new one.");
    }
    return;
  }
  try {
    await authorizeDevice(parsed.origin);
    await bindNativeServer(parsed.origin);
  } catch (setupError) {
    if (setupError?.code === "not_paired") {
      forgetServer();
      fail("Pairing did not finish. Create one new code and try again.");
    } else {
      // The claim already succeeded and is safely in Keychain. Do not tell the
      // owner to spend another rate-limited code for a transient setup failure.
      fail("Paired, but could not open this moa. Check the connection and reopen the app.");
    }
    return;
  }

  location.replace(parsed.origin);
}

function installPairingControls() {
  showPendingShare();
  $("toggle").addEventListener("click", () => {
    $("manual").classList.add("on");
    $("text").focus();
  });

  $("submit").addEventListener("click", () => pair($("text").value));
  $("text").addEventListener("keydown", (e) => {
    if (e.key === "Enter") pair($("text").value);
  });

  $("scan").addEventListener("click", async () => {
    try {
      const result = await scanPairingCode();
      if (result.status === "scanned") {
        await pair(result.value);
      } else if (result.status === "denied") {
        fail("moa needs the camera to scan. Enter the code by hand instead.");
        $("manual").classList.add("on");
      } else if (result.status === "unavailable" || result.status === "unsupported") {
        fail("Camera scanning is not available on this device. Enter the code by hand.");
        $("manual").classList.add("on");
      } else {
        fail("No QR code was found. Try scanning again or enter the code by hand.");
      }
    } catch {
      fail("Could not scan. Enter the code by hand instead.");
      $("manual").classList.add("on");
    }
  });
}

// Already bound: renew a short browser session from Keychain before loading
// the remote page. A revoked credential returns to a usable pairing screen;
// a network failure preserves it for the next launch.
const known = storedServer();
if (known) {
  authorizeDevice(known)
    .then(() => bindNativeServer(known))
    .then(() => location.replace(known))
    .catch(async (authError) => {
      if (authError?.code === "not_paired") {
        forgetServer();
        try { await clearNativeServer(); } catch { /* already unbound is fine */ }
        fail("This device is no longer paired. Create a new code to pair it again.");
        installPairingControls();
        return;
      }
      fail("Could not reach your paired moa. Check the connection and reopen the app.");
    });
} else {
  installPairingControls();
}
