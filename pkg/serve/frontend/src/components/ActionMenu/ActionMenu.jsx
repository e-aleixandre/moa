import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { MOTION } from "../../hooks/motion.js";
import { useMenuKeyboard } from "../../hooks/useMenuKeyboard.js";
import { usePresence } from "../../hooks/usePresence.js";
import "./ActionMenu.css";

// ActionMenu — a small icon trigger that unfurls a list of actions. It carries
// the menu behaviour the mobile chrome used to keep in its own action rail:
// toggle, close on an outside press, close after running an action, and the
// menu/menuitem ARIA that makes the popup readable.
//
// The open state is owned by the caller so the surface hosting the menu can
// close it on its own terms (a send, a session change) without reaching inside.
//
// An action is { id, icon?, label, onClick, active?, closeOnClick? }. `placement="up"`
// opens the list above the trigger, which is what the composer needs: its `+`
// sits at the bottom of the screen with the keyboard under it.
export function ActionMenu({
  actions,
  open,
  onOpenChange,
  icon: TriggerIcon,
  label,
  triggerClass = "",
  triggerSize = 15,
  placement = "down",
  align = "start",
  scrollContainerSelector,
  disabled = false,
}) {
  const rootRef = useRef(null);
  const listRef = useRef(null);
  const triggerRef = useRef(null);
  const [dropUp, setDropUp] = useState(false);
  // The list stays mounted through its exit so it can leave the way it came
  // (motion language, rule 2) instead of vanishing on the frame `open` drops.
  // exitBase, not the default exitFast: the morph collapses the panel back
  // into the button over --motion-exit-base, and a host that unmounts at
  // 140ms cuts it off half-closed. One number, in both places.
  const { mounted, leaving } = usePresence(open, MOTION.exitBase);
  const { onMenuKeyDown } = useMenuKeyboard(open, onOpenChange, triggerRef, listRef);

  useEffect(() => {
    if (!open) return undefined;
    const close = (event) => {
      if (!rootRef.current?.contains(event.target)) onOpenChange(false);
    };
    document.addEventListener("mousedown", close);
    return () => {
      document.removeEventListener("mousedown", close);
    };
  }, [open, onOpenChange]);

  const run = (action) => {
    if (action.closeOnClick !== false) onOpenChange(false);
    action.onClick();
  };

  // Pointer interactions never take focus from the textarea: on the phone that
  // would dismiss the keyboard and reflow the whole screen under the menu.
  // The morph grows the panel from the trigger's size to its own, and CSS
  // cannot animate to `auto`. So the size is measured once, on the frame it
  // mounts, and handed to the animation as two custom properties.
  //
  // useLayoutEffect, not useEffect: this has to land before the browser
  // paints, or the first frame is the panel at full size and the morph plays
  // from a shape the eye already saw.
  useLayoutEffect(() => {
    const node = listRef.current;
    if (!node || !mounted || leaving) return;
    // Measure with the animation suppressed. The first attempt read the node
    // while the morph was already running and got 44px back -- the height of
    // its own opening frame -- so the panel grew to the size of the button and
    // then snapped to full height when the animation ended.
    node.style.animation = "none";
    const { width, height } = node.getBoundingClientRect();
    node.style.setProperty("--action-menu-w", `${Math.round(width)}px`);
    node.style.setProperty("--action-menu-h", `${Math.round(height)}px`);
    node.style.animation = "";
    if (placement === "auto") {
      const scroller = scrollContainerSelector && rootRef.current?.closest(scrollContainerSelector);
      const bottom = scroller ? scroller.getBoundingClientRect().bottom : window.innerHeight;
      const shouldDropUp = height + 8 > bottom - triggerRef.current?.getBoundingClientRect().bottom;
      if (shouldDropUp !== dropUp) setDropUp(shouldDropUp);
    }
  }, [mounted, leaving, actions, dropUp, placement, scrollContainerSelector]);

  useEffect(() => {
    if (!open) setDropUp(false);
  }, [open]);

  const keepFocus = (event) => event.preventDefault();

  return (
    <div class="action-menu" ref={rootRef}>
      <button
        type="button"
        ref={triggerRef}
        class={`action-menu-trigger${triggerClass ? ` ${triggerClass}` : ""}${open ? " is-open" : ""}`}
        aria-label={label}
        title={label}
        aria-haspopup="menu"
        aria-expanded={open}
        disabled={disabled}
        onMouseDown={keepFocus}
        onClick={() => onOpenChange(!open)}
      >
        <TriggerIcon size={triggerSize} aria-hidden="true" />
      </button>
      {mounted && (
        <div
          ref={listRef}
          class={`action-menu-list${(placement === "up" || (placement === "auto" && dropUp)) ? " action-menu-list--up" : ""}${align === "end" ? " action-menu-list--end" : ""}${leaving ? " is-leaving" : ""}`}
          role="menu"
          aria-label={label}
          aria-hidden={leaving || undefined}
          onKeyDown={onMenuKeyDown}
        >
          {actions.map((action) => {
            const Icon = action.icon;
            return (
              <button
                key={action.id}
                type="button"
                role="menuitem"
                class={`action-menu-item${action.active ? " is-active" : ""}${action.danger ? " is-danger" : ""}`}
                aria-pressed={action.active || undefined}
                onMouseDown={keepFocus}
                onClick={() => run(action)}
              >
                {Icon && <Icon size={16} aria-hidden="true" />}
                <span>{action.label}</span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
