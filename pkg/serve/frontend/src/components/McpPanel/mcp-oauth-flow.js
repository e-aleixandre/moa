// mcp-oauth-flow.js — the Connect/Reconnect flow for a remote MCP server that
// is waiting for the user to sign in: start → open the authorize page → the
// user pastes back the address of the callback page → finish.
//
// Popup blockers (iOS Safari above all) only allow window.open inside the
// user's gesture, and the authorize URL arrives after an await. So the click
// handler opens a blank window synchronously (openBlankWindow) and the URL is
// routed into it once it arrives (routeAuthorizeWindow). If the browser blocked
// it, the caller renders the URL as a plain link the user taps instead.

import { MCP_OAUTH_TIMEOUT_MS } from "../../data/api.js";

function serverPath(sessionId, name) {
  return `/api/sessions/${sessionId}/mcp/${encodeURIComponent(name)}/oauth`;
}

// Must be called synchronously from the click handler. Returns null when the
// browser refused to open it.
export function openBlankWindow(win = globalThis.window) {
  try {
    return win?.open?.("", "_blank") || null;
  } catch {
    return null;
  }
}

// Sends an already-open blank window to the authorize URL. The window was
// opened without noopener (that would make open() return null), so the opener
// link is cut here before navigating away. Returns false when there is no
// usable window and the caller must offer a link instead.
export function routeAuthorizeWindow(handle, url) {
  if (!handle || handle.closed) return false;
  try {
    handle.opener = null;
    handle.location.href = url;
    return true;
  } catch {
    return false;
  }
}

export function closeWindow(handle) {
  try {
    if (handle && !handle.closed) handle.close();
  } catch {
    // Nothing to clean up if the browser no longer lets us touch it.
  }
}

// api() errors read "<status>: <body>"; the body is the user-facing message.
export function oauthErrorText(e) {
  const msg = String(e?.message || e || "").trim();
  return msg.replace(/^\d{3}:\s*/, "") || "Something went wrong. Try again.";
}

// Starts the authorization and routes `handle` (opened by openBlankWindow in
// the same click) to it. Resolves to { url, opened }; on failure the blank
// window is closed and the error propagates.
export async function startConnect(api, sessionId, name, handle) {
  let r;
  try {
    r = await api("POST", `${serverPath(sessionId, name)}/start`, null, {
      timeoutMs: MCP_OAUTH_TIMEOUT_MS,
    });
  } catch (e) {
    closeWindow(handle);
    throw e;
  }
  const url = r && typeof r.authorize_url === "string" ? r.authorize_url : "";
  if (!url) {
    closeWindow(handle);
    throw new Error("No sign-in page was returned. Try again.");
  }
  return { url, opened: routeAuthorizeWindow(handle, url) };
}

// Completes the authorization with the address the user pasted and resolves
// to the server's resulting status.
export function finishConnect(api, sessionId, name, pasted) {
  return api(
    "POST",
    `${serverPath(sessionId, name)}/finish`,
    { url: pasted.trim() },
    { timeoutMs: MCP_OAUTH_TIMEOUT_MS }
  );
}
