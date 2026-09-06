// scroll-chain.js — the bookkeeping of the zoomed scroll chain, and nothing
// else: which scroll packets are still waiting for the app's answer, and which
// answers still belong to the gesture the user is making.
//
// While zoomed, a one-finger drag is offered to the app scroller FIRST; what it
// could not take moves the shell's own pan (zoom.js `chainPan`). That answer is
// a round trip, so it can arrive late — just after the finger left, which is
// still the same movement, or after a pinch, a reset or a new frame, which is
// not. `invalidate` draws that line: a generation bumps and every answer minted
// before it is dropped, so a stale packet can never move a view the user has
// meanwhile changed by other means.
//
// postMessage from the one frame is FIFO, so answers arrive in the order the
// requests left. That is why there is no reordering buffer here: an id is a
// receipt, deleted when it is spent, so a valid answer is applied exactly once
// and a duplicate or unknown one is not applied at all.
export const MAX_PENDING = 32;

// Avoid scheduling a preview render when the app consumed all of a packet, or
// when the preview itself is already at the relevant edge.
export function setViewIfChanged(setView, current, next) {
  if (current.zoom === next.zoom && current.x === next.x && current.y === next.y) return false;
  setView(next);
  return true;
}

export function createScrollChain() {
  let generation = 0;
  let seq = 0;
  const pending = new Map();

  return {
    // request — records a packet and returns the identity to send with it.
    request(entry) {
      const id = `s${++seq}`;
      if (pending.size >= MAX_PENDING) pending.delete(pending.keys().next().value);
      pending.set(id, { generation, request: entry });
      return id;
    },
    // resolve — the request this answer belongs to, or null when it is unknown,
    // already spent, from a superseded gesture, or not a pair of numbers.
    resolve(id, consumed) {
      const entry = pending.get(id);
      if (!entry) return null;
      pending.delete(id);
      if (entry.generation !== generation) return null;
      if (!consumed || !Number.isFinite(consumed.dx) || !Number.isFinite(consumed.dy)) return null;
      return entry.request;
    },
    // invalidate — a new gesture, or anything that replaces the view or the
    // frame under it. Also what keeps the map bounded when an app never answers.
    invalidate() {
      generation++;
      pending.clear();
    },
    get pendingCount() {
      return pending.size;
    },
  };
}
