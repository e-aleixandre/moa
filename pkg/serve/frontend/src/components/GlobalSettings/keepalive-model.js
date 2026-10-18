// Pure model of the "Cache keep-alive" settings: what the daily limit field
// accepts and how the money is worded. Wire shape of /api/cache-keepalive:
// { owner, session, subagent: bool, daily_budget_usd, spent_today_usd }.

export const MAX_LIMIT_USD = 1000;

// parseLimit reads what was typed into the daily limit. null = not a usable
// limit, so the caller leaves the stored value alone. Cents are the finest
// grain: a limit of 0.004 would read as "$0.00".
export function parseLimit(raw) {
  const value = Number(String(raw ?? "").trim());
  if (!Number.isFinite(value)) return null;
  const cents = Math.round(value * 100) / 100;
  if (cents <= 0 || cents > MAX_LIMIT_USD) return null;
  return cents;
}

export function formatUsd(value) {
  const n = Number(value);
  return `$${(Number.isFinite(n) ? n : 0).toFixed(2)}`;
}

// spentLine is the "Spent today" reading: what the keep-alive cost so far
// against the limit that stops it.
export function spentLine(spent, limit) {
  return `${formatUsd(spent)} of ${formatUsd(limit)}`;
}

// normalizeKeepAlive tolerates a missing or partial answer: everything off is
// the server's default, so it is also the safe reading.
export function normalizeKeepAlive(raw) {
  return {
    owner: raw?.owner === true,
    session: raw?.session === true,
    subagent: raw?.subagent === true,
    limit: Number.isFinite(raw?.daily_budget_usd) && raw.daily_budget_usd > 0 ? raw.daily_budget_usd : 10,
    spent: Number.isFinite(raw?.spent_today_usd) ? raw.spent_today_usd : 0,
  };
}
