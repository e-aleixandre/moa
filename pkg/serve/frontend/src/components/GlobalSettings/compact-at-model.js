// Pure logic behind the global auto-compaction threshold, kept out of the
// component so the floor rule can be tested directly: the engine silently
// raises any threshold below compact_at_min, so a control that accepted a
// smaller number would promise a compaction point that never happens.

// Thresholds are entered in thousands of tokens. Nobody chooses a context
// limit to the token, and "120k" is how every model window is quoted, so the
// raw token count would only add four zeros to type.
export const TOKENS_PER_UNIT = 1000;

// modeForCompactAt maps a stored threshold onto the segmented control. 0 (or
// missing) is automatic: compaction waits for the model's own window, which is
// how moa behaved before this setting existed.
export function modeForCompactAt(tokens) {
  return tokens > 0 ? "custom" : "auto";
}

// floorUnits is the lowest value the input may offer, rounded UP to the next
// whole unit: rounding down would put the lowest reachable stop back under the
// floor the engine enforces.
export function floorUnits(min) {
  return Math.max(1, Math.ceil((min || 0) / TOKENS_PER_UNIT));
}

// parseUnits reads what the user typed, in thousands of tokens. Returns null
// for anything that is not a usable number, so the caller can leave the stored
// value alone instead of saving a 0 the user never asked for.
export function parseUnits(raw) {
  const value = Number(String(raw ?? "").trim());
  if (!Number.isFinite(value) || value <= 0) return null;
  return Math.round(value);
}

// clampToFloor raises a threshold to the engine's floor and reports whether it
// had to. The caller shows the adjusted number rather than the requested one:
// the server stores it raised too, so displaying the request would be the one
// thing this control must never do — show a limit that will not be honored.
export function clampToFloor(tokens, min) {
  const floor = floorUnits(min) * TOKENS_PER_UNIT;
  if (tokens > 0 && tokens < floor) return { tokens: floor, clamped: true };
  return { tokens, clamped: false };
}

// formatTokens renders a threshold the way the rest of the app quotes windows.
export function formatTokens(tokens) {
  if (!tokens) return "auto";
  return `${Math.round(tokens / TOKENS_PER_UNIT)}k tokens`;
}
