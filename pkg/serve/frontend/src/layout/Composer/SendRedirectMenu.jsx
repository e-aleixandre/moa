import { useEffect, useRef } from "preact/hooks";
import { ListEnd, Zap } from "lucide-preact";
import { registerOverlay } from "../../data/overlays.js";

// SendRedirectMenu — what a long-press on Send offers while the agent works.
// TEMPORARY, prototype variant B only (data/proto-steer-ui.js): it is deleted
// with the rest of that variant if A is chosen.
//
// Two verbs, because a short tap already means "queue": the menu exists to name
// the OTHER one, so both rows say what happens to the agent's pending steps.
// It is not ActionMenu: that one owns its trigger and has no room for a second
// line, and this one is opened by a gesture on a button that already exists.
//
// Pointer interactions never take focus from the textarea (the keyboard on the
// phone would drop and reflow the screen), hence the mousedown guard.
let seq = 0;

export function SendRedirectMenu({ onPick, onClose }) {
  const idRef = useRef(null);
  if (idRef.current === null) idRef.current = `send-redirect-menu-${++seq}`;
  const rootRef = useRef(null);

  useEffect(() => {
    const outside = (event) => {
      if (!rootRef.current?.contains(event.target)) onClose();
    };
    // Capture phase and stopPropagation: with the caret in the box, an Escape
    // that reached the textarea would also be the keyboard's Stop and abort the
    // run — closing this menu must be the whole of what that key does here.
    const onKey = (event) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopPropagation();
      onClose();
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("keydown", onKey, true);
    const unregister = registerOverlay(idRef.current);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("keydown", onKey, true);
      unregister();
    };
  }, [onClose]);

  const keepFocus = (event) => event.preventDefault();
  return (
    <div class="zl-send-menu" ref={rootRef} role="menu" aria-label="How to send">
      <button type="button" role="menuitem" class="zl-send-menu-row" onMouseDown={keepFocus} onClick={() => onPick(false)}>
        <ListEnd size={16} aria-hidden="true" />
        <span class="zl-send-menu-text">
          <span class="zl-send-menu-label">Queue</span>
          <span class="zl-send-menu-sub">Read at the next step</span>
        </span>
      </button>
      <button type="button" role="menuitem" class="zl-send-menu-row is-redirect" onMouseDown={keepFocus} onClick={() => onPick(true)}>
        <Zap size={16} aria-hidden="true" />
        <span class="zl-send-menu-text">
          <span class="zl-send-menu-label">Redirect now</span>
          <span class="zl-send-menu-sub">Pending steps won't run</span>
        </span>
      </button>
    </div>
  );
}
