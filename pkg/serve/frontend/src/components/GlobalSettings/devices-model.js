// Pure model for the paired-devices list: what a credential IS right now, how
// it is ordered, and how its two dates are said in words.
//
// Kept out of the component for the same reason settings-rows.js is: the
// decisions here are the ones a later change is most likely to break by
// accident, and they can be stated without a DOM. Everything below reads only
// the fields /api/pulse/devices actually returns (pkg/serve/device_auth.go,
// `devicePublic`): id, label, issued_at, expires_at, revoked_at?,
// last_used_at?. There is no "this is the device in your hand" flag on the
// wire, and none is invented here — see DevicesPage for why this screen does
// not need one.

import { clockMs } from "../../data/util/clock.js";

const MINUTE = 60000;
const HOUR = 3600000;
const DAY = 86400000;

// A credential is in exactly one of these. Revoked wins over expired because
// it is the thing the owner DID; expired is what time did.
export const ACTIVE = "active";
export const EXPIRED = "expired";
export const REVOKED = "revoked";

// deviceState — which of the three a record is in, judged against `now` rather
// than trusting a flag the server does not send. `expires_at` is authoritative
// on the server too (device_auth.go: authenticate refuses a credential whose
// expiry has passed), so the client says the same thing the server would.
export function deviceState(device, now = Date.now()) {
  if (!device) return EXPIRED;
  if (device.revoked_at) return REVOKED;
  const expires = clockMs(device.expires_at);
  if (expires !== null && expires <= now) return EXPIRED;
  return ACTIVE;
}

// The colour a state is allowed to wear, in the product's established
// semantics: green = ok/idle, red = error, and grey for a credential that is
// simply no longer anything. Revoking is not an error, so a revoked device is
// quiet rather than red — the red in this section is reserved for the ACTION,
// which is the destructive part.
export const STATE_TONE = {
  [ACTIVE]: "ok",
  [EXPIRED]: "gone",
  [REVOKED]: "gone",
};

// sortDevices — active first, and inside each group the most recently seen
// first. A credential that was used two minutes ago is the one the owner is
// looking for when something is wrong; a revoked one from March is archive.
//
// Never mutates the argument: the caller's array comes straight from a fetch
// and is also the previous render's state.
export function sortDevices(devices, now = Date.now()) {
  const rank = { [ACTIVE]: 0, [EXPIRED]: 1, [REVOKED]: 2 };
  return [...(devices || [])].sort((a, b) => {
    const byState = rank[deviceState(a, now)] - rank[deviceState(b, now)];
    if (byState !== 0) return byState;
    return (lastSeenMs(b) || 0) - (lastSeenMs(a) || 0);
  });
}

function lastSeenMs(device) {
  return clockMs(device?.last_used_at) ?? clockMs(device?.issued_at);
}

// countActive — the reading the root row shows. Only active credentials count:
// a revoked one has no access, and printing it in the total would say the
// opposite of what the row means.
export function countActive(devices, now = Date.now()) {
  return (devices || []).filter((device) => deviceState(device, now) === ACTIVE).length;
}

// devicesValue — what the "Devices" row in Access shows on its right, in the
// same grammar as every other row's reading: the answer, not the setting's
// name. `null` means "not read yet" and the row draws its loading state, the
// one the other rows already use. A request that FAILED says nothing rather
// than "0 devices", which would be a lie the owner could act on.
export function devicesValue(devices, loaded, failed = false, now = Date.now()) {
  if (!loaded) return null;
  if (failed) return "—";
  const active = countActive(devices, now);
  if (active === 0) return "None";
  return active === 1 ? "1 device" : `${active} devices`;
}

// markRevoked — the list as it is the instant the owner confirms, before the
// server has answered. Revoking is the one thing on this screen that must feel
// immediate, and the record it produces is fully known here: the server sets
// revoked_at and changes nothing else (device_auth.go, deviceStore.revoke).
//
// Order is PRESERVED on purpose: re-sorting would drop the row to the bottom
// of the list under the owner's finger and shift everything below it. The row
// changes in place; the next load puts it where it belongs.
export function markRevoked(devices, id, at = Date.now()) {
  return (devices || []).map((device) =>
    device.id === id && !device.revoked_at
      ? { ...device, revoked_at: new Date(at).toISOString() }
      : device);
}

// loadFailure — what to SAY when the list could not be read. Three answers,
// because the owner can act on three different things.
//
// 403 is the one that matters and it is not a fault: administering pairings is
// reserved to the owner's own token (route_auth.go, routeOwnerAdmin), so a
// paired phone asking for this list is refused BY DESIGN. The screen says that
// plainly instead of drawing an error — nothing is broken, this device simply
// is not where the question is answered.
export function loadFailure(error) {
  const status = error?.status;
  if (status === 403) {
    return {
      kind: "forbidden",
      title: "Not available on a paired device",
      detail: "Open moa with the server's token to manage pairings.",
    };
  }
  if (status === 503) {
    return {
      kind: "unavailable",
      title: "Pairing is unavailable",
      detail: "The server has no device store right now.",
    };
  }
  return {
    kind: "error",
    title: "Could not read the device list",
    detail: "Check the connection and try again.",
  };
}

// relAge — how long ago, in the short form the session list already uses
// (layout/Sidebar/sessions.js). Restated rather than imported because that one
// is private to the row's second line; the shape is the product's convention.
export function relAge(value, now = Date.now()) {
  const ms = clockMs(value);
  if (ms === null) return "";
  const diff = now - ms;
  if (diff < MINUTE) return "just now";
  if (diff < HOUR) return `${Math.floor(diff / MINUTE)}m ago`;
  if (diff < DAY) return `${Math.floor(diff / HOUR)}h ago`;
  const days = Math.floor(diff / DAY);
  if (days < 7) return `${days}d ago`;
  if (days < 30) return `${Math.floor(days / 7)}w ago`;
  return `${Math.floor(days / 30)}mo ago`;
}

// untilLabel — how long a credential has left. Days, because the credential
// lasts 180 of them (deviceCredentialTTL) and an hour-level reading would be
// false precision on a number nobody acts on hourly. Under a day it says so.
export function untilLabel(value, now = Date.now()) {
  const ms = clockMs(value);
  if (ms === null) return "";
  const diff = ms - now;
  if (diff <= 0) return "expired";
  if (diff < HOUR) return "under an hour";
  if (diff < DAY) return `${Math.floor(diff / HOUR)}h`;
  const days = Math.round(diff / DAY);
  return days === 1 ? "1 day" : `${days} days`;
}

// lifeFraction — how much of a credential's life is spent, 0…1, for the meter
// that draws it. Derived from the two dates the server sends rather than from
// the 180-day constant, so a pairing issued with a shorter TTL (the API takes
// device_expires_days) draws its own life and not somebody else's.
export function lifeFraction(device, now = Date.now()) {
  const issued = clockMs(device?.issued_at);
  const expires = clockMs(device?.expires_at);
  if (issued === null || expires === null || expires <= issued) return 1;
  return Math.min(1, Math.max(0, (now - issued) / (expires - issued)));
}

// A credential near its end is worth a different colour: it will stop working
// on its own, and the owner would rather re-pair on purpose than be locked out
// on a Tuesday. Fourteen days is the window — long enough to act without
// hurrying, short enough that it is not permanently yellow.
export const EXPIRING_SOON_MS = 14 * DAY;

export function expiringSoon(device, now = Date.now()) {
  const expires = clockMs(device?.expires_at);
  if (expires === null) return false;
  return expires > now && expires - now <= EXPIRING_SOON_MS;
}

// deviceLine — the one line under a device's name. It answers the question the
// screen exists for ("does this thing have access right now, and when did it
// last use it"), never repeating the name and never inventing a field.
export function deviceLine(device, now = Date.now()) {
  const state = deviceState(device, now);
  if (state === REVOKED) return `Revoked ${relAge(device.revoked_at, now)}`;
  if (state === EXPIRED) return `Expired ${relAge(device.expires_at, now)}`;
  const seen = device.last_used_at
    ? `Last used ${relAge(device.last_used_at, now)}`
    : `Paired ${relAge(device.issued_at, now)}`;
  return seen;
}

// deviceKind — which glyph a credential wears. Read from the LABEL the client
// chose at claim time, which is the only thing about the hardware that ever
// reaches the server: the native app sends "moa app (iPhone)" / "(iPad)"
// (mobile/www/boot.js, deviceLabel). Anything unrecognised is "unknown" and
// gets the neutral glyph — a guess dressed as a fact would be worse than a
// shrug, since this screen exists to be trusted.
export function deviceKind(label) {
  const text = String(label || "").toLowerCase();
  if (text.includes("ipad") || text.includes("tablet")) return "tablet";
  if (text.includes("iphone") || text.includes("android") || text.includes("phone")) return "phone";
  if (text.includes("mac") || text.includes("laptop") || text.includes("windows") || text.includes("linux")) return "computer";
  return "unknown";
}
