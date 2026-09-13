// Which shell moa is running in, decided once at startup.
//
// The levels are described in tokens/shell.css. This module answers one
// question -- may the app paint edge to edge? -- and it answers it
// conservatively: only a native container is granted `edge`, because only a
// native container was measured to actually own the whole screen.
//
// An installed iOS PWA looks like it qualifies and does not: it reports
// standalone display-mode, resolves safe-area insets, paints under the status
// bar with black-translucent, and still receives a window 62px shorter than
// the screen that no CSS can extend (WebKit 313800). Granting it `edge` is
// exactly the trap that produced a dead black strip under the composer.

export const SHELL_BROWSER = "browser";
export const SHELL_PWA = "pwa";
export const SHELL_NATIVE = "native";

// detectShell — read the environment. Injected globals are checked defensively
// because this runs before anything else and must never throw.
export function detectShell(win = typeof window !== "undefined" ? window : null) {
  if (!win) return SHELL_BROWSER;

  // Capacitor announces itself on the window it creates. isNativePlatform() is
  // the documented call; the property fallbacks cover older bridges.
  const cap = win.Capacitor;
  if (cap) {
    try {
      if (typeof cap.isNativePlatform === "function") {
        if (cap.isNativePlatform()) return SHELL_NATIVE;
      } else if (cap.isNative === true) {
        return SHELL_NATIVE;
      }
    } catch {
      // A broken bridge is not a native platform.
    }
  }

  // No runtime on this page does not mean no container. The container loads
  // moa from the server, and Capacitor only injects window.Capacitor into
  // pages it serves itself -- so a remote page inside a perfectly good
  // container would see nothing. What is always there is the native bridge the
  // web view was built with, which is what Capacitor itself tests for
  // (@capacitor/core getPlatformId).
  if (win.webkit?.messageHandlers?.bridge || win.androidBridge) {
    return SHELL_NATIVE;
  }

  const standalone = win.navigator?.standalone === true
    || win.matchMedia?.("(display-mode: standalone)")?.matches === true;
  return standalone ? SHELL_PWA : SHELL_BROWSER;
}

// ownsScreen — whether this shell may bleed past the safe area. Kept separate
// from the shell name so the policy lives in one expression: if another
// container is added later, this is the only line that decides.
export function ownsScreen(shell) {
  return shell === SHELL_NATIVE;
}

// applyShell — publish the decision to CSS. One class, read by shell.css.
export function applyShell(shell, doc = typeof document !== "undefined" ? document : null) {
  if (!doc) return shell;
  doc.documentElement.classList.toggle("shell-edge", ownsScreen(shell));
  doc.documentElement.dataset.shell = shell;
  return shell;
}
