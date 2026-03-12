import { useEffect } from 'preact/hooks';

/** Detect mac for shortcut labels and modifier key */
export const isMac = typeof navigator !== 'undefined' &&
  /Mac|iPhone|iPad/.test(navigator.userAgent);

/**
 * Global keyboard shortcut handler.
 * Bindings: array of { key, mod?, shift?, handler, when? }
 *   key: e.key value (case-insensitive match)
 *   mod: requires the platform modifier (⌘ on Mac, Alt on Linux/Windows)
 *   shift: requires Shift
 *   handler: () => void
 *   when: optional () => bool guard
 *
 * We use Alt on non-Mac to avoid Ctrl+<key> conflicts with browser shortcuts
 * (Ctrl+N = new window, Ctrl+T = new tab, etc.)
 */
export function useHotkeys(bindings) {
  useEffect(() => {
    function onKeyDown(e) {
      const inInput = e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA' ||
                      e.target.isContentEditable;

      // Platform modifier: Cmd on Mac, Alt on everything else
      const modPressed = isMac ? e.metaKey : e.altKey;

      for (const b of bindings) {
        if (e.key.toLowerCase() !== b.key.toLowerCase()) continue;

        const wantMod = !!b.mod;
        const wantShift = !!b.shift;
        if (wantMod !== modPressed) continue;
        if (wantShift !== e.shiftKey) continue;

        // Skip plain keys (no modifier) when focused on an input
        if (inInput && !wantMod) continue;

        // Guard
        if (b.when && !b.when()) continue;

        e.preventDefault();
        e.stopPropagation();
        b.handler();
        return;
      }
    }

    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [bindings]);
}

/** Format a shortcut for display: ⌘B or Alt+B */
export function formatShortcut(key, { mod, shift } = {}) {
  const parts = [];
  if (mod) parts.push(isMac ? '⌘' : 'Alt+');
  if (shift) parts.push(isMac ? '⇧' : 'Shift+');
  parts.push(key.toUpperCase());
  return parts.join('');
}
