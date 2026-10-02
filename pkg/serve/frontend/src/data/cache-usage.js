// cache-usage.js — the prompt cache's reading, and the one sentence it says.
//
// WHY THIS EXISTS: a session ran 130 turns writing cache and reading none. It
// wrote 15.6M tokens and burned a weekly plan in eleven hours, and nothing on
// screen said a word. The ratio is not decorative telemetry; the streak is a
// cost alarm.
//
// The numbers are NOT computed here. `core.SummarizeCacheUsage` computes them
// server-side over the whole display history and they arrive on the init
// snapshot and the `cache_usage` event. The client's bounded transcript
// (150 messages) would measure a truncated tail — wrong in exactly the long
// sessions this is for — so this module only READS `session.cacheUsage`.
//
// The house rule of the dossier applies: `available: false` is "no reading
// yet", not 0%. A 0% that is really "no data" reads like a diagnosis.

// The alert is the STREAK and nothing else. No percentage threshold and no
// per-provider logic: real ratios run from 48% to 96% depending on provider,
// so a fixed threshold would fire on healthy sessions and stay silent on the
// broken one. Consecutive turns that write and never read is the shape of the
// failure regardless of provider.
export const CACHE_STREAK_ALERT = 3;

const EMPTY = {
  available: false, ratio: 0, read: 0, written: 0, streak: 0, alert: false,
  misses: 0, missCostUSD: 0, lastMiss: null,
};

// parseLastMiss reads the wire shape of one cache miss. A summary without a
// miss, or one this client cannot read, is null: the panel prints nothing
// rather than a half-filled line.
function parseLastMiss(m) {
  if (!m || typeof m !== 'object' || typeof m.cause !== 'string') return null;
  return {
    cause: m.cause,
    gapSeconds: Number(m.gap_seconds) || 0,
    tokens: Number(m.tokens) || 0,
    costUSD: Number(m.cost_usd) || 0,
    atMs: Number(m.at_ms) || 0,
  };
}

// parseCacheUsage turns the server's `cache_usage` payload (init snapshot and
// event share it) into the session field. The one place the wire shape is read.
export function parseCacheUsage(d) {
  return {
    available: !!d?.available,
    ratio: Number(d?.ratio) || 0,
    read: Number(d?.read) || 0,
    written: Number(d?.written) || 0,
    streak: Number(d?.streak) || 0,
    alert: !!d?.alert,
    misses: Number(d?.misses) || 0,
    missCostUSD: Number(d?.miss_cost_usd) || 0,
    lastMiss: parseLastMiss(d?.last_miss),
  };
}

// cacheUsage reads the session's summary, defaulting to "no reading" rather
// than to zeros that would print as a ratio.
export function cacheUsage(session) {
  const u = session?.cacheUsage;
  if (!u || typeof u !== 'object') return EMPTY;
  return {
    available: !!u.available,
    ratio: Number(u.ratio) || 0,
    read: Number(u.read) || 0,
    written: Number(u.written) || 0,
    streak: Number(u.streak) || 0,
    // Trust the server's verdict, but never alert on a summary that has no
    // data behind it: a streak cannot exist without turns that reported usage.
    alert: !!u.alert && !!u.available,
    misses: Number(u.misses) || 0,
    missCostUSD: Number(u.missCostUSD) || 0,
    lastMiss: u.lastMiss && typeof u.lastMiss === 'object' ? u.lastMiss : null,
  };
}

// The cause of a miss, as the one phrase the panel shows. `unknown` says so:
// a cause is only named when the transcript demonstrates it.
const MISS_CAUSE = {
  compaction: 'after compaction',
  model_changed: 'model changed',
  context_cut: 'context cut',
  unknown: 'no known cause',
};

// fmtGap writes an idle gap the way a person says it: "7m", "1h 12m", "2d 3h".
export function fmtGap(seconds) {
  const s = Math.max(0, Math.round(Number(seconds) || 0));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`;
  const d = Math.floor(h / 24);
  return h % 24 ? `${d}d ${h % 24}h` : `${d}d`;
}

// cacheMissCause is the phrase for a miss's cause; an expiry carries the idle
// time that explains it.
export function cacheMissCause(miss) {
  if (!miss) return '';
  if (miss.cause === 'expired') return `idle ${fmtGap(miss.gapSeconds)}`;
  return MISS_CAUSE[miss.cause] || MISS_CAUSE.unknown;
}

// cacheRatioPercent is the ratio as a whole percentage, or null when there is
// no reading. Null rather than 0 so a caller cannot print it by accident.
export function cacheRatioPercent(session) {
  const u = cacheUsage(session);
  if (!u.available) return null;
  return Math.round(u.ratio * 100);
}

// cacheVerdict is the one line the Usage row shows on the panel's root: the
// fact, not an explanation of prompt caching. When the streak is alerting it
// says how many turns, because "12 turns without a cache read" tells you both
// that something is wrong and how long it has been wrong — a lit dot tells you
// neither.
export function cacheVerdict(session) {
  const u = cacheUsage(session);
  if (!u.available) return { text: 'no cache reading yet', warn: false };
  if (u.alert) {
    // tone 'warn' is yellow. The panel row's default warn colour is red, which
    // belongs to something broken; a cache streak is expensive, not broken.
    return { text: `${u.streak} turns without a cache read`, warn: true, tone: 'warn' };
  }
  return { text: `${Math.round(u.ratio * 100)}% cache hit`, warn: false };
}

// cacheAlertLabel is the accessible name of the indicator on the panel button.
// It names the session's problem and where the tap goes, because the button
// itself only carries a dot.
export function cacheAlertLabel(session) {
  const u = cacheUsage(session);
  if (!u.alert) return '';
  return `${u.streak} turns without a cache read; open Usage`;
}

// cacheAdvice instructs the next action rather than explaining the mechanism.
// The user does not need to know what a cache breakpoint is; they need to know
// that every turn is being re-billed in full and that starting a fresh session
// is what stops it.
export function cacheAdvice(session) {
  const u = cacheUsage(session);
  if (!u.alert) return '';
  return 'Every turn is being billed as new input. Start a fresh session to stop it.';
}
