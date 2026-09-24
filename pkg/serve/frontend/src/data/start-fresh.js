// start-fresh.js — "Start fresh": cut the model's context without a summary.
//
// The server keeps the whole transcript and records a marker whose place is
// right before the first message the model still sees (first_kept_msg_id). A
// full reload already draws it there; these helpers put a live or resumed
// marker in the same place, and tell the composer when the action is spent.

// placeFreshMarker inserts a normalized fresh-marker row before the row of the
// first kept message, or appends it when that message is not loaded. A row
// already present (same _msg_id) is left alone.
export function placeFreshMarker(messages, row) {
  if (!row?._msg_id || messages.some(m => m?._msg_id === row._msg_id)) return messages;
  const at = row.firstKept ? messages.findIndex(m => m?._msg_id === row.firstKept) : -1;
  if (at < 0) return [...messages, row];
  return [...messages.slice(0, at), row, ...messages.slice(at)];
}

// settleFreshMarkers moves fresh markers that arrived in a history delta (the
// server appends them where the cut entry was recorded, because the cut point
// is older than the delta) to their place before the first kept message.
export function settleFreshMarkers(messages) {
  const markers = messages.filter(m => m?.systemType === 'fresh_marker' && m.firstKept);
  if (markers.length === 0) return messages;
  let out = messages;
  for (const marker of markers) {
    const without = out.filter(m => m !== marker);
    if (!without.some(m => m?._msg_id === marker.firstKept)) continue;
    out = placeFreshMarker(without, marker);
  }
  return out;
}
