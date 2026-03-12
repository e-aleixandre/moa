import { useEffect } from 'preact/hooks';

/**
 * Global keyboard shortcut handler.
 * Bindings: array of { key, ctrl?, shift?, handler, when? }
 *   key: e.key value (case-insensitive match)
 *   ctrl: requires Cmd (mac) or Ctrl
 *   shift: requires Shift
 *   handler: () => void
 *   when: optional () => bool guard
 */
export function useHotkeys(bindings) {
  useEffect(() => {
    function onKeyDown(e) {
      // Don't intercept when typing in inputs (unless it's a modifier combo)
      const inInput = e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA' ||
                      e.target.isContentEditable;
      const hasMod = e.metaKey || e.ctrlKey;

      for (const b of bindings) {
        // Key match (case-insensitive)
        if (e.key.toLowerCase() !== b.key.toLowerCase()) continue;

        // Modifier match
        const wantCtrl = !!b.ctrl;
        const wantShift = !!b.shift;
        if (wantCtrl !== hasMod) continue;
        if (wantShift !== e.shiftKey) continue;

        // Skip plain keys when focused on an input
        if (inInput && !hasMod) continue;

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

/** Detect mac for shortcut labels */
export const isMac = typeof navigator !== 'undefined' &&
  /Mac|iPhone|iPad/.test(navigator.userAgent);

/** Format a shortcut for display: ⌘B or Ctrl+B */
export function formatShortcut(key, { ctrl, shift } = {}) {
  const parts = [];
  if (ctrl) parts.push(isMac ? '⌘' : 'Ctrl+');
  if (shift) parts.push(isMac ? '⇧' : 'Shift+');
  parts.push(key.toUpperCase());
  return parts.join('');
}
