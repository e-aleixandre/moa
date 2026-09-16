// Haptics, where the platform has any.
//
// navigator.vibrate is the web's only answer and iOS does not implement it --
// not in Safari, not in an installed web app. The calls scattered through this
// codebase have therefore never done anything on the device moa is mostly used
// from. Inside the native container there is a real Taptic Engine behind the
// Capacitor bridge, and this module is the one place that knows the
// difference.
//
// The vocabulary is intent, not waveform: callers say what happened, and each
// platform answers with whatever it has. A drawer crossing its threshold is a
// `select`; a message arriving is a `notify`. Nothing here describes
// milliseconds, because the two backends do not agree on what those mean.

const NATIVE_STYLE = { light: "LIGHT", medium: "MEDIUM", heavy: "HEAVY" };

// Web fallbacks, in milliseconds. Android honours these; iOS ignores them.
const WEB_PATTERN = {
  select: 12,
  impact: 30,
  notify: [0, 60, 40, 60],
};

// The bridge is resolved lazily and once: the plugin only exists in the
// container, and importing it eagerly would fail the web build.
let plugin;
let resolved = false;

function haptics() {
  if (resolved) return plugin;
  resolved = true;
  try {
    plugin = globalThis.Capacitor?.Plugins?.Haptics || null;
  } catch {
    plugin = null;
  }
  return plugin;
}

// tap — a discrete tick. `kind` is one of select | impact | notify.
// Never throws and never awaits: feedback that arrives late is worse than
// none, and a gesture must not be held up by a bridge call.
export function tap(kind = "select") {
  const native = haptics();
  if (native) {
    try {
      if (kind === "notify") {
        native.notification({ type: "SUCCESS" });
      } else if (kind === "impact") {
        native.impact({ style: NATIVE_STYLE.medium });
      } else {
        // Capacitor's iOS plugin only creates its UISelectionFeedbackGenerator
        // in selectionStart; selectionChanged by itself resolves successfully
        // but deliberately does nothing. A discrete detent still needs the
        // complete lifecycle, even though it has only one change.
        native.selectionStart();
        native.selectionChanged();
        native.selectionEnd();
      }
      return true;
    } catch {
      // Fall through: a failed bridge call should still try the web path.
    }
  }

  const pattern = WEB_PATTERN[kind] ?? WEB_PATTERN.select;
  try {
    if (typeof navigator !== "undefined" && navigator.vibrate) {
      navigator.vibrate(pattern);
      return true;
    }
  } catch {
    // Some browsers throw when vibrating without user activation.
  }
  return false;
}

// hasHaptics — whether a tap will be felt. For deciding whether an interaction
// may rely on touch alone, not for choosing a pattern.
export function hasHaptics() {
  if (haptics()) return true;
  return typeof navigator !== "undefined" && typeof navigator.vibrate === "function";
}

// resetForTest clears the memoised bridge lookup.
export function resetForTest() {
  resolved = false;
  plugin = undefined;
}
