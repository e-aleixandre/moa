// Attention occurrence IDs are process-global on the server, but UI arrival
// ordering needs to stay monotonic even when different sessions report them.
// Keep the client-side presentation counter global and dedupe each server
// occurrence identity so a following poll cannot replay a WS arrival. Server
// occurrence sequences are monotonic only within one runtime namespace, so each
// per-session high-water record is scoped to the namespace that minted it. A
// restart therefore makes a lower, genuinely new occurrence arrive once.
let nextArrival = 0;
const sessionHighWater = new Map();

export function attentionArrival(sessionID, unseenSeq, namespace = '') {
  if (!sessionID || !unseenSeq) return 0;
  const known = sessionHighWater.get(sessionID);
  if (known && known.namespace === namespace && unseenSeq <= known.sequence) return known.arrival;
  const arrival = ++nextArrival;
  sessionHighWater.set(sessionID, { namespace, sequence: unseenSeq, arrival });
  return arrival;
}

export function forgetAttentionArrival(sessionID) {
  sessionHighWater.delete(sessionID);
}

export function retainAttentionArrivals(sessionIDs) {
  for (const id of sessionHighWater.keys()) {
    if (!sessionIDs.has(id)) sessionHighWater.delete(id);
  }
}

export function __resetAttentionArrivalsForTests() {
  nextArrival = 0;
  sessionHighWater.clear();
}
