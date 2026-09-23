// The listener opens on demand. Each browser reaches it through its own host,
// rather than reusing another device's saved address.

const REQUEST = { "Content-Type": "application/json", "X-Moa-Request": "1" };

// Same scheme and host as Moa, with the separately exposed preview port.
export function suggestPublicURL(location, port) {
  if (!location || !port) return "";
  const protocol = location.protocol === "https:" ? "https:" : "http:";
  const host = location.hostname;
  if (!host) return "";
  const bracketed = host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
  return `${protocol}//${bracketed}:${port}`;
}

export async function fetchPreviewStatus(fetchImpl = fetch) {
  const response = await fetchImpl("/api/preview/target", { cache: "no-store", credentials: "same-origin" });
  if (!response.ok) throw new Error("preview status failed");
  return response.json();
}

// activatePreview opens the listener (if it is not already up) and points it at
// the dev server, in one request: there is no state where the port is open but
// nothing is being previewed.
export async function activatePreview(fetchImpl, { url, publicURL, port, parentOrigin }) {
  const body = { url, parent_origin: parentOrigin };
  if (publicURL) body.public_url = publicURL;
  if (port) body.port = port;
  const response = await fetchImpl("/api/preview/target", {
    method: "PUT",
    headers: REQUEST,
    body: JSON.stringify(body),
    cache: "no-store",
    credentials: "same-origin",
  });
  if (!response.ok) {
    const detail = (await response.text().catch(() => "")).trim();
    throw new Error(detail || "The preview proxy could not be started.");
  }
  return response.json();
}

// An opaque no-CORS response is enough: the capability-protected listener
// answers even unauthenticated requests, while a closed or unexposed port fails.
export async function checkPreviewReachable(fetchImpl, publicURL, timeoutMs = 5000) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    await fetchImpl(publicURL, { mode: "no-cors", credentials: "omit", cache: "no-store", signal: controller.signal });
  } finally {
    clearTimeout(timer);
  }
}

// deactivatePreview closes the listener and every connection through it. It is
// sent with keepalive so leaving the page still takes the port down.
export function deactivatePreview(fetchImpl = fetch) {
  return fetchImpl("/api/preview/target", {
    method: "PUT",
    headers: REQUEST,
    body: JSON.stringify({ enabled: false }),
    cache: "no-store",
    credentials: "same-origin",
    keepalive: true,
  });
}

// INSPECTOR_NOTICE — shown when the inspector in the previewed page never
// answered. The old wording explained the mechanism ("add the inspector
// script"); what the user needs is the next action.
export const INSPECTOR_NOTICE = "Reload the preview. If the notice stays, open Preview options and check the app URL.";
