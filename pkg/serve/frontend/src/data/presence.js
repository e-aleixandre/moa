// Presence: tells the server whether this client has a session on screen, so a
// question you are already looking at does not also buzz your phone. The server
// lets a report expire (~45 s), so a visible client keeps renewing it and a
// client that vanishes without a word stops counting by itself.

const WS_OPEN = 1;
export const PRESENCE_RENEW_MS = 15000;

export function presenceFrame(doc = globalThis.document) {
  return JSON.stringify({ type: 'presence', visible: doc?.visibilityState === 'visible' });
}

export function sendPresence(ws, doc) {
  if (ws && ws.readyState === WS_OPEN) ws.send(presenceFrame(doc));
}

// Reports on every change of visibility and, while visible, renews on a timer.
// `sockets` returns the live sockets; returns a function that stops it.
export function watchPresence(sockets, doc = globalThis.document) {
  if (!doc) return () => {};
  const all = () => { for (const ws of sockets()) sendPresence(ws, doc); };
  const timer = setInterval(() => { if (doc.visibilityState === 'visible') all(); }, PRESENCE_RENEW_MS);
  doc.addEventListener('visibilitychange', all);
  return () => { clearInterval(timer); doc.removeEventListener('visibilitychange', all); };
}
