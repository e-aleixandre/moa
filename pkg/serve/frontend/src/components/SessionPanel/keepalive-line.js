import { fmtCost } from "../../data/util/usage-pills.js";

// keepAliveLine is the keep-alive row of the spend breakdown, or null while
// the session has had no ping: its cost, how many, and what they saved.
export function keepAliveLine(session) {
  const pings = Number(session?.keepAlivePings) || 0;
  if (pings <= 0) return null;
  const saved = Number(session?.keepAliveSavedUSD) || 0;
  return {
    value: fmtCost(Number(session?.keepAliveCostUSD) || 0),
    detail: `${pings} ${pings === 1 ? "ping" : "pings"}`,
    note: saved > 0 ? `Saved about ${fmtCost(saved)} on cold starts.` : "",
  };
}
