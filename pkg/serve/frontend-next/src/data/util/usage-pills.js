// usage-pills — pure selectors + formatters for the StatusStrip's plan-usage
// telemetry (phase 5O). This is the DUAL-source logic ported from the old
// TaskBar, kept UI-free so it can be unit-tested in isolation.
//
// The two providers report plan usage from different places:
//   • Anthropic (provider empty or 'anthropic'): the 5h/weekly windows and the
//     extra-usage spend come from the GLOBAL /api/usage snapshot (shared across
//     sessions), which also carries reset timestamps.
//   • OpenAI/Codex (provider 'openai'): there is NO usage endpoint, so the
//     5h/weekly meters come from the per-session rate-limit headers
//     (session.rlFiveHourPct / rlSevenDayPct) — percent only, no reset time.
//
// usageForSession collapses both into ONE shape the StatusStrip renders without
// caring which provider it came from:
//
//   {
//     fiveHour: { pct:number, resetsAt:string|null, source:'anthropic'|'openai' } | null,
//     week:     { pct:number, resetsAt:string|null, source:'anthropic'|'openai' } | null,
//     extra:    { enabled:bool, used:number, limit:number|null,
//                 currency:string, decimalPlaces:number } | null,
//     onOverage: bool,
//   }
//
// Guards: a null / unavailable global snapshot yields null anthropic meters (we
// never fake a window). Missing/negative rl* percents yield null openai meters.
// `extra` is null unless the Anthropic snapshot reports extra usage as enabled.

/** usageLevel buckets a percent into a severity class name suffix:
 *  ≥80 → 'high', ≥50 → 'med', otherwise 'low'. */
export function usageLevel(pct) {
  return pct >= 80 ? "high" : pct >= 50 ? "med" : "low";
}

/** fmtReset renders the time until an ISO reset timestamp as a compact
 *  "2d 3h" / "4h 5m" / "30m" / "now". A missing/invalid iso → ''. */
export function fmtReset(iso) {
  if (!iso) return "";
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return "";
  const ms = t - Date.now();
  if (!(ms > 0)) return "now";
  const m = Math.floor(ms / 60000);
  const h = Math.floor(m / 60);
  const d = Math.floor(h / 24);
  if (d > 0) return `${d}d ${h % 24}h`;
  if (h > 0) return `${h}h ${m % 60}m`;
  return `${m}m`;
}

function currencySymbol(cur) {
  switch ((cur || "").toUpperCase()) {
    case "":
    case "USD":
      return "$";
    case "EUR":
      return "€";
    case "GBP":
      return "£";
    default:
      return cur + " ";
  }
}

/** money formats a value the endpoint reports in MINOR currency units (e.g.
 *  cents) into a display string, scaling by decimal_places (default 2) and
 *  prefixing the currency symbol (USD $ / EUR € / GBP £, else "CODE "). */
export function money(minor, extra) {
  const dp = extra && Number.isInteger(extra.decimal_places) ? extra.decimal_places : 2;
  const major = (minor ?? 0) / Math.pow(10, dp);
  return `${currencySymbol(extra && extra.currency)}${major.toFixed(2)}`;
}

/** fmtCost mirrors the TUI cost segment: a sub-cent spend shows 4 decimals so a
 *  fraction of a cent stays visible, otherwise 2. */
export function fmtCost(usd) {
  const v = usd ?? 0;
  return v < 0.01 ? `$${v.toFixed(4)}` : `$${v.toFixed(2)}`;
}

/** usageForSession — see file header for the returned contract. Pure: reads
 *  only `session` (per-session fields) and `globalUsage` (the /api/usage
 *  snapshot, or null before the first poll). */
export function usageForSession(session, globalUsage) {
  const s = session || {};
  const isOpenAI = s.provider === "openai";
  const isAnthropic = !s.provider || s.provider === "anthropic";
  const u = globalUsage && globalUsage.available ? globalUsage : null;

  let fiveHour = null;
  let week = null;
  let extra = null;

  if (isAnthropic && u) {
    if (u.five_hour && Number.isFinite(u.five_hour.utilization)) {
      fiveHour = {
        pct: Math.round(u.five_hour.utilization),
        resetsAt: u.five_hour.resets_at || null,
        source: "anthropic",
      };
    }
    if (u.seven_day && Number.isFinite(u.seven_day.utilization)) {
      week = {
        pct: Math.round(u.seven_day.utilization),
        resetsAt: u.seven_day.resets_at || null,
        source: "anthropic",
      };
    }
    if (u.extra_usage && u.extra_usage.is_enabled) {
      const e = u.extra_usage;
      extra = {
        enabled: true,
        used: e.used_credits ?? 0,
        limit: e.monthly_limit != null ? e.monthly_limit : null,
        currency: e.currency || "USD",
        decimalPlaces: Number.isInteger(e.decimal_places) ? e.decimal_places : 2,
      };
    }
  }

  if (isOpenAI) {
    if (typeof s.rlFiveHourPct === "number" && s.rlFiveHourPct >= 0) {
      fiveHour = { pct: Math.round(s.rlFiveHourPct), resetsAt: null, source: "openai" };
    }
    if (typeof s.rlSevenDayPct === "number" && s.rlSevenDayPct >= 0) {
      week = { pct: Math.round(s.rlSevenDayPct), resetsAt: null, source: "openai" };
    }
  }

  return { fiveHour, week, extra, onOverage: !!s.onOverage };
}
