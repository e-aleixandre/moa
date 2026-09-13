// Which moa this app talks to.
//
// The web frontend is served by the moa it belongs to, so it never has to ask.
// A downloaded app has no such luck: it is the same client with no server, and
// the address cannot be compiled in or every install would point at whoever
// built it.
//
// So the app is unbound until it is paired. The pairing envelope already
// carries everything needed -- the server origin and a one-time payload, see
// data/pulse-pairing.js -- because the browser that generated it was, by
// definition, already talking to the right server.

const STORAGE_KEY = "moa-server";
const ENVELOPE_PREFIX = "moa-link-v1:";

// decodeEnvelope — read a scanned or pasted pairing envelope.
// Returns null rather than throwing: this parses untrusted input from a camera,
// and a misread QR is an ordinary event, not an error.
export function decodeEnvelope(text) {
  if (typeof text !== "string") return null;
  const trimmed = text.trim();
  if (!trimmed.startsWith(ENVELOPE_PREFIX)) return null;

  const encoded = trimmed.slice(ENVELOPE_PREFIX.length);
  let json;
  try {
    const base64 = encoded.replace(/-/g, "+").replace(/_/g, "/");
    const padded = base64 + "=".repeat((4 - (base64.length % 4)) % 4);
    // Decoded as UTF-8 rather than through escape(), which is deprecated and
    // mangles any non-ASCII byte -- a hostname with an accent would not
    // survive it.
    const bytes = Uint8Array.from(atob(padded), (c) => c.charCodeAt(0));
    json = JSON.parse(new TextDecoder().decode(bytes));
  } catch {
    return null;
  }

  const url = typeof json?.server_url === "string" ? json.server_url.trim() : "";
  const payload = typeof json?.pairing_payload === "string" ? json.pairing_payload.trim() : "";
  if (!url || !payload) return null;

  // Only https, and only an origin. A pairing code that can point the app at
  // plain http is a pairing code that can be downgraded on a hostile network;
  // one carrying a path could aim it at an endpoint of someone else's
  // choosing.
  let origin;
  try {
    const parsed = new URL(url);
    if (parsed.protocol !== "https:") return null;
    origin = parsed.origin;
  } catch {
    return null;
  }

  return { origin, payload };
}

// readManual — the way out when a camera is not an option: the panel that
// shows the QR can also copy the origin and the payload as two lines.
export function readManual(text) {
  if (typeof text !== "string") return null;
  const lines = text.split("\n").map((l) => l.trim()).filter(Boolean);
  if (lines.length < 2) return null;

  const [url, payload] = lines;
  try {
    const parsed = new URL(url);
    if (parsed.protocol !== "https:") return null;
    return { origin: parsed.origin, payload };
  } catch {
    return null;
  }
}

// parsePairing — accept either shape without making the person say which one
// they have. A scan produces an envelope; a paste is usually the manual form.
export function parsePairing(text) {
  return decodeEnvelope(text) || readManual(text);
}

export function storedServer(storage = globalThis.localStorage) {
  try {
    const raw = storage?.getItem(STORAGE_KEY);
    if (!raw) return null;
    const parsed = new URL(raw);
    return parsed.protocol === "https:" ? parsed.origin : null;
  } catch {
    return null;
  }
}

export function rememberServer(origin, storage = globalThis.localStorage) {
  try {
    storage?.setItem(STORAGE_KEY, origin);
    return true;
  } catch {
    return false;
  }
}

export function forgetServer(storage = globalThis.localStorage) {
  try {
    storage?.removeItem(STORAGE_KEY);
  } catch {
    // Nothing to do: an unbindable app is still usable, it just asks again.
  }
}
