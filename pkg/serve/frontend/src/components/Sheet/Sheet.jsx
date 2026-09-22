import { useEffect, useRef } from "preact/hooks";
import { createPortal } from "preact/compat";
import { X } from "lucide-preact";
import { IconButton } from "../../primitives/index.js";
import { useSheetLayer } from "../../hooks/useSheetLayer.js";
import { useStore } from "../../hooks/useStore.js";
import { usePresence } from "../../hooks/usePresence.js";
import { MOTION } from "../../hooks/motion.js";
import "./Sheet.css";

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

// Sheet — centered modal panel with overlay. Closes with Escape and click on the
// overlay, traps focus while open and restores it to the trigger on close.
// While open, it registers itself as the top layer (data/overlay-layers.js) so
// a capture-phase key handler BELOW it (the artifacts drawer) knows to leave
// Escape to this Sheet, and keys reach only the top one when two are open. On
// a phone it is also under the one-sheet rule (hooks/useSheetLayer.js): asked
// for over another sheet, it covers that one instead of stacking on it.
// `page` marks a Sheet that is a full-screen surface on the phone (the live
// preview): a sheet over it is still the only sheet, so it is never covered.
// It does not touch browser history: a back/forward gesture has no meaning in
// the installed app, and every Sheet already closes through Escape, its X or
// the backdrop.
//
// The overlay is PORTALLED to <body>. `.sheet-overlay` is position:fixed with
// --z-sheet (200), above the composer/dock's --z-overlay (100), but z-index is
// only comparable inside the same stacking context: rendered in place, a Sheet
// opened from deep inside the chat stream (e.g. FileCard's preview) is buried
// in a subtree that is a SIBLING of the composer and agent dock inside .mconv,
// so the browser painted those on top of it regardless of z-index. Escaping to
// <body> puts the overlay in the root stacking context, where --z-sheet
// actually wins.
export function Sheet({ open, onClose, title, ariaLabel, "aria-label": ariaLabelAttr, children, class: className, page = false, ...rest }) {
  const panelRef = useRef(null);
  const previousFocusRef = useRef(null);
  const label = ariaLabel ?? ariaLabelAttr ?? title;
  // The generic Sheet had no motion at all: rewind, the secrets dialog and the
  // file lightbox simply appeared and vanished. It is the least frequent of
  // the overlays and the most jarring when it happens.
  const presence = usePresence(open, MOTION.exitBase);
  // The desktop keeps its centred modal exactly as it was: only the phone
  // gets the one-sheet rule.
  const isMobile = useStore((s) => s.isMobile);
  const oneSheet = isMobile && !page;
  const layer = useSheetLayer({ open, present: open || presence.mounted, sheet: oneSheet });
  // The panel is only in the DOM from the commit after `open` (usePresence),
  // so on `open` alone there is nothing to focus. Under the one-sheet rule the
  // focus would stay behind on the sheet this one covers, now hidden — so on
  // the phone focus waits for the panel. The desktop keeps its timing.
  const focusReady = oneSheet ? presence.mounted : true;

  // requestClose is the single close path (Escape, backdrop, close button): it
  // drops the layer claim immediately, so an overlay below stops deferring to
  // this Sheet even before the unmount effect runs.
  const requestClose = () => {
    layer.close();
    onClose?.();
  };

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (e) => {
      // Keys belong to the top layer only: with a sheet covered below this
      // one, Escape must close what is on screen, not both.
      if (!layer.takesKey(e)) return;
      if (e.key === "Escape") {
        requestClose();
        return;
      }
      if (e.key !== "Tab") return;
      const panel = panelRef.current;
      if (!panel) return;
      const focusable = Array.from(panel.querySelectorAll(FOCUSABLE_SELECTOR)).filter(
        (el) => el.offsetParent !== null || el === document.activeElement
      );
      if (focusable.length === 0) {
        e.preventDefault();
        panel.focus();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [open, onClose]);

  useEffect(() => {
    if (!open || !focusReady) return;
    previousFocusRef.current = document.activeElement;
    const panel = panelRef.current;
    if (panel) {
      const firstFocusable = panel.querySelector(FOCUSABLE_SELECTOR);
      (firstFocusable || panel).focus();
    }
    return () => {
      const toRestore = previousFocusRef.current;
      if (toRestore && typeof toRestore.focus === "function") {
        toRestore.focus();
      }
    };
  }, [open, focusReady]);

  if (!presence.mounted) return null;

  const onOverlayClick = (e) => {
    if (e.target === e.currentTarget) requestClose();
  };

  const overlay = (
    <div class={`sheet-overlay${presence.leaving ? " is-leaving" : ""}`} onClick={onOverlayClick} ref={layer.rootRef}>
      <div
        class={`sheet${className ? ` ${className}` : ""}${presence.leaving ? " is-leaving" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-label={label}
        tabIndex={-1}
        ref={panelRef}
        {...rest}
      >
        {title && (
          <div class="sheet-head">
            <h3>{title}</h3>
            <IconButton label="Close" onClick={requestClose}>
              <X size={15} />
            </IconButton>
          </div>
        )}
        <div class="sheet-body">{children}</div>
      </div>
    </div>
  );

  // No document (SSR / non-DOM test env): render in place rather than crash;
  // the stacking problem only exists in a real browser anyway.
  if (typeof document === "undefined" || !document.body) return overlay;
  return createPortal(overlay, document.body);
}
