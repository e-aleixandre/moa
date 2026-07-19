import { useEffect, useRef } from "preact/hooks";
import { X } from "lucide-preact";
import { IconButton } from "../../primitives/index.js";
import { openOverlay } from "../../data/overlay-history.js";
import "./Sheet.css";

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

let sheetIdCounter = 0;

// Sheet — centered modal panel with overlay. Closes with Escape and click on the
// overlay, traps focus while open and restores it to the trigger on close.
// While open, it registers itself with the shared overlay-history stack
// (data/overlay-history.js) so the browser/PWA back gesture closes it instead
// of navigating away — every consumer (RewindTimeline, file/HTML viewers,
// drawers…) gets that for free just by using Sheet.
export function Sheet({ open, onClose, title, ariaLabel, "aria-label": ariaLabelAttr, children, ...rest }) {
  const panelRef = useRef(null);
  const previousFocusRef = useRef(null);
  const closeOverlayRef = useRef(null);
  const idRef = useRef(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const label = ariaLabel ?? ariaLabelAttr ?? title;
  if (idRef.current === null) idRef.current = `sheet-${++sheetIdCounter}`;

  // Register/unregister with overlay-history whenever `open` toggles. Closing
  // via popstate (back gesture) calls onClose the same as any other close
  // path — the overlay-history module already made sure not to double-pop.
  // Reads onClose through a ref so an identity change while open doesn't
  // tear down and re-push a history entry.
  useEffect(() => {
    if (!open) return undefined;
    closeOverlayRef.current = openOverlay(idRef.current, () => onCloseRef.current?.());
    return () => {
      // Cleanup covers unmount-while-open too, not just the normal close
      // path — either way the history entry must be consumed.
      closeOverlayRef.current?.();
      closeOverlayRef.current = null;
    };
  }, [open]);

  // requestClose funnels every close path (Escape, backdrop, close button)
  // through overlay-history so the pushed entry gets consumed with a single
  // history.back() — bypassing it here would leave a dangling entry.
  const requestClose = () => {
    closeOverlayRef.current?.();
    onClose?.();
  };

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (e) => {
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
    if (!open) return;
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
  }, [open]);

  if (!open) return null;

  const onOverlayClick = (e) => {
    if (e.target === e.currentTarget) requestClose();
  };

  return (
    <div class="sheet-overlay" onClick={onOverlayClick}>
      <div
        class="sheet"
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
              <X size={16} />
            </IconButton>
          </div>
        )}
        <div class="sheet-body">{children}</div>
      </div>
    </div>
  );
}
