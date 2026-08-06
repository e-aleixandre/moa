// Attention occurrence IDs are process-global on the server, but UI arrival
// ordering needs to stay monotonic even when different sessions report them.
// Keep the client-side presentation counter global and dedupe each server
// occurrence identity so a following poll cannot replay a WS arrival. Server
// occurrence IDs are monotonic only within one server process, so each
// per-session high-water record is scoped to the server instance that minted
// it. A restart therefore makes a lower, genuinely new generation arrive once.
let nextArrival = 0;
const sessionHighWater = new Map();

export function attentionArrival(sessionID, unseenGen, serverInstance = '') {
  if (!sessionID || !unseenGen) return 0;
  const known = sessionHighWater.get(sessionID);
  if (known && known.serverInstance === serverInstance && unseenGen <= known.generation) return known.arrival;
  const arrival = ++nextArrival;
  sessionHighWater.set(sessionID, { serverInstance, generation: unseenGen, arrival });
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
