import { useEffect, useRef } from "preact/hooks";
import { X } from "lucide-preact";
import { IconButton } from "../../primitives/index.js";
import "./Sheet.css";

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

// Sheet — centered modal panel with overlay. Closes with Escape and click on the
// overlay, traps focus while open and restores it to the trigger
// on close. Doesn't do scroll-lock or manage history yet (that comes with
// the mobile integration, out of scope for this block) — the structure
// is ready to hook it up.
export function Sheet({ open, onClose, title, ariaLabel, "aria-label": ariaLabelAttr, children, ...rest }) {
  const panelRef = useRef(null);
  const previousFocusRef = useRef(null);
  const label = ariaLabel ?? ariaLabelAttr ?? title;

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (e) => {
      if (e.key === "Escape") {
        onClose?.();
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
    if (e.target === e.currentTarget) onClose?.();
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
            <IconButton label="Close" onClick={onClose}>
              <X size={16} />
            </IconButton>
          </div>
        )}
        <div class="sheet-body">{children}</div>
      </div>
    </div>
  );
}
