// Browser-local proof that a pending request was actually displayed. It is
// deliberately not an arrival cache: without a visible receipt it must not
// survive a reload and become an acknowledgement.

const PREFIX = "moa-pending-attention:";
const MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000;
const MAX_ENTRIES = 100;
// Storage is an optimization for reloads, not the authority for a receipt in
// this tab. Keep foreground proof here too: quota/privacy failures must not
// make a resolved card unmount and cancel the only retry that can clear its
// unread dot.
const memory = new Map();

function storage() {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch (_) {
    return null;
  }
}

function valid(value) {
  const seenAt = value?.seenAt || 0;
  return !!(value?.id && (value.unseenGen ?? value.unseen_gen) && seenAt > 0 && Date.now() - seenAt <= MAX_AGE_MS);
}

function prune(store, reserve = 0) {
  const entries = [];
  for (let i = 0; i < (store.length || 0); i++) {
    const key = store.key(i);
    if (!key?.startsWith(PREFIX)) continue;
    try {
      const value = JSON.parse(store.getItem(key) || "null");
      if (!valid(value)) store.removeItem(key);
      else entries.push({ key, seenAt: value.seenAt, confirmed: !!value.confirmed });
    } catch (_) {
      store.removeItem(key);
    }
  }
  // A pending receipt is proof for a request that may still be live on the
  // server. It is never a safe eviction candidate: losing it can strand an
  // unread dot after reload. Confirmed records are only retained for legacy
  // compatibility and are the sole capacity victims after expired records.
  const confirmed = entries.filter((entry) => entry.confirmed).sort((a, b) => a.seenAt - b.seenAt);
  for (const entry of confirmed) {
    if (entries.length + reserve <= MAX_ENTRIES) break;
    store.removeItem(entry.key);
    entries.splice(entries.indexOf(entry), 1);
  }
  return entries.length + reserve <= MAX_ENTRIES;
}

export function rememberedPendingAttention(sessionId) {
	const inMemory = memory.get(sessionId);
	if (valid(inMemory)) return inMemory;
	if (inMemory) memory.delete(sessionId);
	const store = storage();
  if (!store || !sessionId) return null;
  try {
    const key = PREFIX + sessionId;
    const value = JSON.parse(store.getItem(key) || "null");
    if (!valid(value)) {
      store.removeItem(key);
      return null;
    }
    return value;
  } catch (_) {
    return null;
  }
}

// Returns false when durable proof cannot be written. A caller that has just
// established foreground visibility may still acknowledge from that in-memory
// proof; false only means a reload cannot inherit it. At capacity we preserve
// every potentially live receipt rather than evicting one for the newcomer.
export function rememberPendingAttention(sessionId, pending) {
	if (!sessionId || !pending?.id) return false;
	const unseenGen = pending.unseen_gen ?? pending.unseenGen ?? 0;
	if (!unseenGen) return false;
	const value = {
		...pending,
		unseenGen,
		serverInstance: pending.server_instance ?? pending.serverInstance ?? "",
		seenAt: Date.now(),
	};
	memory.set(sessionId, value);
	const store = storage();
	if (!store) return false;
	try {
		const key = PREFIX + sessionId;
		const replacing = !!store.getItem(key);
		if (!prune(store, replacing ? 0 : 1)) return false;
		store.setItem(key, JSON.stringify(value));
    return store.getItem(key) === JSON.stringify(value);
  } catch (_) {
    return false;
  }
}

export function forgetPendingAttention(sessionId) {
	memory.delete(sessionId);
  try {
    storage()?.removeItem(PREFIX + sessionId);
  } catch (_) {
    // A stale proof is fenced by generation and server instance on read.
  }
}
