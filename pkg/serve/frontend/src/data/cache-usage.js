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

const EMPTY = { available: false, ratio: 0, read: 0, written: 0, streak: 0, alert: false };

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
  };
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
