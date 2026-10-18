// Pure model of the "Cache keep-alive" settings. Wire shape of
// /api/cache-keepalive: { owner, session, subagent: bool, spent_today_usd,
// saved_today_usd }.

export function formatUsd(value) {
  const n = Number(value);
  return `$${(Number.isFinite(n) ? n : 0).toFixed(2)}`;
}

// normalizeKeepAlive tolerates a missing or partial answer: everything off is
// the server's default, so it is also the safe reading.
export function normalizeKeepAlive(raw) {
  return {
    owner: raw?.owner === true,
    session: raw?.session === true,
    subagent: raw?.subagent === true,
    spent: Number.isFinite(raw?.spent_today_usd) ? raw.spent_today_usd : 0,
    saved: Number.isFinite(raw?.saved_today_usd) ? raw.saved_today_usd : 0,
  };
}
