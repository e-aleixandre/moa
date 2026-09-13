// Binding the app to a moa, then getting out of the way.
//
// This runs only in the container's local page. Once a server is known the
// window is sent there and the real frontend takes over -- including the next
// launch, which never sees this screen again.

import { parsePairing, storedServer, rememberServer } from "./pairing.js";

const $ = (id) => document.getElementById(id);
const error = $("error");

function fail(message) {
  error.textContent = message;
}

// claim — turn a one-time payload into a credential this device keeps.
// The route is deliberately unauthenticated: the short-lived secret in the
// envelope is the authority (see pkg/serve/route_auth.go).
async function claim(origin, payload) {
  const [, pairingID, pairingSecret] = payload.split(":");
  if (!pairingID || !pairingSecret) throw new Error("malformed");

  const response = await fetch(`${origin}/api/pulse/pairings/claim`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Moa-Request": "1" },
    body: JSON.stringify({
      pairing_id: pairingID,
      pairing_secret: pairingSecret,
      device_label: deviceLabel(),
    }),
  });
  if (!response.ok) throw new Error(await response.text());
  return response.json();
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
  } catch {
    // A pairing code is short-lived and single-use, which is the likeliest
    // reason to be here -- worth saying, rather than "something went wrong".
    fail("Could not pair. The code may have expired; create a new one.");
    return;
  }

  rememberServer(parsed.origin);
  location.replace(parsed.origin);
}

// Already bound: go straight through, without showing the pairing screen.
const known = storedServer();
if (known) {
  location.replace(known);
} else {
  $("toggle").addEventListener("click", () => {
    $("manual").classList.add("on");
    $("text").focus();
  });

  $("submit").addEventListener("click", () => pair($("text").value));
  $("text").addEventListener("keydown", (e) => {
    if (e.key === "Enter") pair($("text").value);
  });

  $("scan").addEventListener("click", async () => {
    const scanner = globalThis.Capacitor?.Plugins?.BarcodeScanner;
    if (!scanner) {
      fail("No camera here. Enter the code by hand.");
      $("manual").classList.add("on");
      return;
    }
    try {
      const granted = await scanner.requestPermissions?.();
      if (granted && granted.camera === "denied") {
        fail("moa needs the camera to scan. Enter the code by hand instead.");
        $("manual").classList.add("on");
        return;
      }
      const result = await scanner.scan({ formats: ["QR_CODE"] });
      const value = result?.barcodes?.[0]?.rawValue;
      if (value) await pair(value);
    } catch {
      fail("Could not scan. Enter the code by hand instead.");
      $("manual").classList.add("on");
    }
  });
}
