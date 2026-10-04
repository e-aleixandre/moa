// providers-flow.js — the requests behind Settings → Providers. Each is one
// POST, never repeated by this module: a sign-in exchange or a key write whose
// answer did not arrive may still have happened, so a timeout is reported and
// the page re-reads the status instead of sending it again.
//
// Nothing here keeps what passes through it. Keys and pasted values are
// arguments, sent once, and gone with the call: no module state, no storage,
// no logging.

import { openBlankWindow, routeAuthorizeWindow, closeWindow } from "../SessionPanel/mcp-oauth-flow.js";

export { openBlankWindow, closeWindow };

// Exchanges talk to the provider; the server bounds them well inside this.
export const PROVIDER_ACTION_TIMEOUT_MS = 45000;
export const PROGRESS_INTERVAL_MS = 2000;

const base = (provider) => `/api/providers/${encodeURIComponent(provider)}`;

export function listProviders(api) {
  return api("GET", "/api/providers", null, { cache: "no-store" });
}

// beginSignIn — starts an attempt. For the paste flows `handle` is the blank
// window opened synchronously in the click (see mcp-oauth-flow.js); it is cut
// from this page and sent to the authorize address here. `opened` false means
// the caller must offer a link instead.
// `isCurrent` false when the answer arrives means the caller moved on (Cancel,
// unmount): the window is closed, nothing is navigated, and `stale` is set.
export async function beginSignIn(api, provider, generation, handle = null, isCurrent = () => true) {
  let attempt;
  try {
    attempt = await api("POST", `${base(provider)}/oauth/begin`, { expected_generation: generation || "" }, {
      timeoutMs: PROVIDER_ACTION_TIMEOUT_MS,
    });
  } catch (e) {
    closeWindow(handle);
    throw e;
  }
  if (!attempt || typeof attempt.attempt_id !== "string") {
    closeWindow(handle);
    throw new Error("No sign-in was returned. Try again.");
  }
  if (!isCurrent()) {
    closeWindow(handle);
    return { attempt, opened: false, stale: true };
  }
  if (attempt.flow === "device") {
    closeWindow(handle);
    return { attempt, opened: false };
  }
  const url = typeof attempt.authorize_url === "string" ? attempt.authorize_url : "";
  return { attempt, opened: url ? routeAuthorizeWindow(handle, url) : false };
}

export function completeSignIn(api, provider, attemptId, input) {
  return api("POST", `${base(provider)}/oauth/complete`, { attempt_id: attemptId, input: String(input || "").trim() }, {
    timeoutMs: PROVIDER_ACTION_TIMEOUT_MS,
  });
}

export function signInProgress(api, provider, attemptId) {
  return api("POST", `${base(provider)}/oauth/progress`, { attempt_id: attemptId });
}

// cancelSignIn — best effort: the attempt also ends on its own deadline.
export function cancelSignIn(api, provider, attemptId) {
  if (!attemptId) return Promise.resolve();
  return api("POST", `${base(provider)}/oauth/cancel`, { attempt_id: attemptId }).catch(() => {});
}

export function saveApiKey(api, provider, key, generation) {
  return api("POST", `${base(provider)}/api-key`, { key: String(key || "").trim(), expected_generation: generation || "" }, {
    timeoutMs: PROVIDER_ACTION_TIMEOUT_MS,
  });
}

export function retrySave(api, provider) {
  return api("POST", `${base(provider)}/retry-save`, {}, { timeoutMs: PROVIDER_ACTION_TIMEOUT_MS });
}

// copyText — the device code to the clipboard. Only ever the user code: the
// one value that is meant to be typed somewhere else.
export function copyText(text, nav = globalThis.navigator) {
  try {
    return nav?.clipboard?.writeText ? nav.clipboard.writeText(text).then(() => true, () => false) : Promise.resolve(false);
  } catch {
    return Promise.resolve(false);
  }
}
