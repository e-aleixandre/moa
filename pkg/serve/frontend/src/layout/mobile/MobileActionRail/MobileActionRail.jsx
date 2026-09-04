import { useEffect, useRef, useState } from "preact/hooks";
import { MoreVertical } from "lucide-preact";
import "./MobileActionRail.css";

// Session actions share the small amount of room beside the mobile title.
// An action is { id, icon, label, onClick, badge?, active?, visible? }.
export function MobileActionRail({ actions }) {
  const [overflowOpen, setOverflowOpen] = useState(false);
  const railRef = useRef(null);
  const visibleActions = actions.filter((action) => action.visible !== false);
  if (!visibleActions.length) return null;

  const pending = visibleActions.reduce((total, action) => total + (Number(action.badge) || 0), 0);

  useEffect(() => {
    if (!overflowOpen) return undefined;
    const close = (event) => {
      if (!railRef.current?.contains(event.target)) setOverflowOpen(false);
    };
    document.addEventListener("mousedown", close);
    return () => document.removeEventListener("mousedown", close);
  }, [overflowOpen]);

  const run = (action) => {
    setOverflowOpen(false);
    action.onClick();
  };

  return (
    <div class="mconv-action-rail" ref={railRef}>
      <button
        type="button"
        class={`mconv-action-rail-button${overflowOpen ? " is-active" : ""}`}
        aria-label={pending ? `Session actions, ${pending} waiting` : "Session actions"}
        aria-haspopup="menu"
        aria-expanded={overflowOpen}
        onClick={() => setOverflowOpen((open) => !open)}
      >
        <MoreVertical size={18} aria-hidden="true" />
        {pending > 0 && <span class="mconv-action-rail-badge" aria-hidden="true">{pending > 9 ? "9+" : pending}</span>}
      </button>
      {overflowOpen && (
        <div class="mconv-action-rail-overflow" role="menu" aria-label="Session actions">
          {visibleActions.map((action) => <ActionButton key={action.id} action={action} menuItem onClick={() => run(action)} />)}
        </div>
      )}
    </div>
  );
}

function ActionButton({ action, menuItem = false, onClick }) {
  const Icon = action.icon;
  const badge = action.badge > 0 ? (action.badge > 9 ? "9+" : action.badge) : null;
  return (
    <button
      type="button"
      class={`mconv-action-rail-button${menuItem ? " is-menu-item" : ""}${action.active ? " is-active" : ""}`}
      aria-label={badge ? `${action.label}, ${action.badge} waiting` : action.label}
      aria-pressed={action.active || undefined}
      role={menuItem ? "menuitem" : undefined}
      onClick={onClick}
    >
      <Icon size={18} aria-hidden="true" />
      {menuItem && <span>{action.label}</span>}
      {badge && <span class="mconv-action-rail-badge" aria-hidden="true">{badge}</span>}
    </button>
  );
}
