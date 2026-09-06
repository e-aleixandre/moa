import { useEffect, useRef } from "preact/hooks";
import "./ActionMenu.css";

// ActionMenu — a small icon trigger that unfurls a list of actions. It carries
// the menu behaviour the mobile chrome used to keep in its own action rail:
// toggle, close on an outside press, close after running an action, and the
// menu/menuitem ARIA that makes the popup readable.
//
// The open state is owned by the caller so the surface hosting the menu can
// close it on its own terms (a send, a session change) without reaching inside.
//
// An action is { id, icon, label, onClick, active? }. `placement="up"` opens the
// list above the trigger, which is what the composer needs: its `+` sits at the
// bottom of the screen with the keyboard under it.
export function ActionMenu({
  actions,
  open,
  onOpenChange,
  icon: TriggerIcon,
  label,
  triggerClass = "",
  triggerSize = 15,
  placement = "down",
  disabled = false,
}) {
  const rootRef = useRef(null);

  useEffect(() => {
    if (!open) return undefined;
    const close = (event) => {
      if (!rootRef.current?.contains(event.target)) onOpenChange(false);
    };
    const onKey = (event) => {
      if (event.key === "Escape") onOpenChange(false);
    };
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", onKey);
    };
  }, [open, onOpenChange]);

  const run = (action) => {
    onOpenChange(false);
    action.onClick();
  };

  // The trigger and its items never take focus from the textarea: on the phone
  // that would dismiss the keyboard and reflow the whole screen under the menu.
  const keepFocus = (event) => event.preventDefault();

  return (
    <div class="action-menu" ref={rootRef}>
      <button
        type="button"
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
      {open && (
        <div class={`action-menu-list${placement === "up" ? " action-menu-list--up" : ""}`} role="menu" aria-label={label}>
          {actions.map((action) => {
            const Icon = action.icon;
            return (
              <button
                key={action.id}
                type="button"
                role="menuitem"
                class={`action-menu-item${action.active ? " is-active" : ""}`}
                aria-pressed={action.active || undefined}
                onMouseDown={keepFocus}
                onClick={() => run(action)}
              >
                <Icon size={16} aria-hidden="true" />
                <span>{action.label}</span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
