import { MoreHorizontal } from "lucide-preact";
import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { copyToClipboard } from "../../data/util/format.js";
import { useMenuKeyboard } from "../../hooks/useMenuKeyboard.js";
import "./SessionCardMenu.css";

// SessionCardMenu keeps session lifecycle actions with the session card while
// leaving card selection to the caller. It owns the popup lifecycle so mobile
// and desktop retain identical click-outside, Escape, and keyboard behavior.
export function SessionCardMenu({
  session,
  onClose,
  onReopen,
  onDelete,
  scrollContainerSelector,
}) {
  const [open, setOpen] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [dropUp, setDropUp] = useState(false);
  const ref = useRef(null);
  const actionsRef = useRef(null);
  const triggerRef = useRef(null);
  const { onMenuKeyDown, closeMenu } = useMenuKeyboard(open, setOpen, triggerRef, actionsRef);

  useEffect(() => {
    if (!open) return undefined;
    const onDocDown = (event) => {
      if (ref.current && !ref.current.contains(event.target)) setOpen(false);
    };
    document.addEventListener("mousedown", onDocDown);
    return () => document.removeEventListener("mousedown", onDocDown);
  }, [open]);

  useLayoutEffect(() => {
    if (!open) {
      setDropUp(false);
      return;
    }
    const button = triggerRef.current;
    const menu = actionsRef.current;
    if (!button || !menu) return;
    const scroller = scrollContainerSelector && ref.current?.closest(scrollContainerSelector);
    const bottom = scroller ? scroller.getBoundingClientRect().bottom : window.innerHeight;
    setDropUp(menu.offsetHeight + 8 > bottom - button.getBoundingClientRect().bottom);
  }, [open, confirmingDelete, scrollContainerSelector]);

  useEffect(() => {
    if (!open) setConfirmingDelete(false);
  }, [open]);

  const stop = (event) => event.stopPropagation();

  return (
    <div class="session-card-menu" ref={ref} onClick={stop}>
      <button
        type="button"
        class="session-card-menu-button"
        ref={triggerRef}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="Session actions"
        onClick={(event) => { stop(event); setOpen((value) => !value); }}
      >
        <MoreHorizontal size={16} aria-hidden="true" />
      </button>
      {open && (
        <div
          class={dropUp ? "session-card-actions session-card-actions--up" : "session-card-actions"}
          role="menu"
          aria-label="Session actions"
          ref={actionsRef}
          onKeyDown={onMenuKeyDown}
        >
          {session.saved ? (
            <button type="button" role="menuitem" class="session-card-action" onClick={() => { closeMenu(); onReopen?.(session.id); }}>
              Reopen session
            </button>
          ) : (
            <button type="button" role="menuitem" class="session-card-action" onClick={() => { closeMenu(); onClose?.(session.id); }}>
              Close session
            </button>
          )}
          <button type="button" role="menuitem" class="session-card-action" onClick={() => { copyToClipboard(session.id); closeMenu(); }}>
            Copy session ID
          </button>
          {confirmingDelete ? (
            <button type="button" role="menuitem" class="session-card-action session-card-action-danger" onClick={() => { closeMenu(); onDelete?.(session.id); }}>
              Delete — this cannot be undone
            </button>
          ) : (
            <button type="button" role="menuitem" class="session-card-action session-card-action-danger" onClick={(event) => { stop(event); setConfirmingDelete(true); }}>
              Delete…
            </button>
          )}
        </div>
      )}
    </div>
  );
}
