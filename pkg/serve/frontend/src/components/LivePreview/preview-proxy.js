// preview-proxy — the client half of Live Preview's hot activation.
//
// The proxy listener is not started with the server: it is opened when someone
// actually loads a URL in the panel and closed when they leave. Moa cannot
// derive on its own the address a browser uses to reach that listener (a
// tailnet name, a reverse proxy, a LAN IP), so the first time it is needed the
// panel proposes one built from the address the user is already on, and the
// user confirms or corrects it. After that it is remembered server-side and
// never asked again.

const REQUEST = { "Content-Type": "application/json", "X-Moa-Request": "1" };

// suggestPublicURL builds the proposal: same scheme and host the user reached
// Moa through, with the proxy's port. Someone on
// https://dev.taild072ac.ts.net:7401 gets https://dev.taild072ac.ts.net:7402 —
// the one guess that is right whenever the port is exposed the same way Moa is.
export function suggestPublicURL(location, port) {
  if (!location || !port) return "";
  const protocol = location.protocol === "https:" ? "https:" : "http:";
  const host = location.hostname;
  if (!host) return "";
  const bracketed = host.includes(":") && !host.startsWith("[") ? `[${host}]` : host;
  return `${protocol}//${bracketed}:${port}`;
}

// portOf reads the port back out of an edited address, so a user who types
// ":9000" moves the listener instead of pointing a 7402 listener at nothing.
export function portOf(publicURL) {
  try {
    const parsed = new URL(publicURL);
    if (parsed.port) return Number(parsed.port);
    return parsed.protocol === "https:" ? 443 : 80;
  } catch {
    return 0;
  }
}

// validPublicURL — an absolute http(s) origin. Rejected here so a typo is a
// message next to the field instead of a round trip and a raw server error.
export function validPublicURL(raw) {
  try {
    const parsed = new URL((raw || "").trim());
    return (parsed.protocol === "http:" || parsed.protocol === "https:") && !!parsed.hostname;
  } catch {
    return false;
  }
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

// setupStep decides what the panel asks for, in order: the app URL, then the
// address of the proxy — and the second question only ever appears once.
export function setupStep({ status, savedURL, editing }) {
  if (editing === "url") return "url";
  if (editing === "address") return "address";
  if (!savedURL) return "url";
  if (status && status.supported && !status.public_url) return "address";
  return null;
}

// INSPECTOR_NOTICE — shown when the inspector in the previewed page never
// answered. The old wording explained the mechanism ("add the inspector
// script"); what the user needs is the next action.
export const INSPECTOR_NOTICE = "Reload the preview. If the notice stays, open Preview options and check the app URL.";
