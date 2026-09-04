import { useState } from "preact/hooks";
import { MoreVertical } from "lucide-preact";
import "./MobileActionRail.css";

// Session actions share the small amount of room beside the mobile title.
// An action is { id, icon, label, onClick, badge?, active?, visible? }.
export function MobileActionRail({ actions, maxVisible = 2 }) {
  const [overflowOpen, setOverflowOpen] = useState(false);
  const visibleActions = actions.filter((action) => action.visible !== false);
  const hasOverflow = visibleActions.length > maxVisible;
  const directActions = visibleActions.slice(0, hasOverflow ? maxVisible - 1 : maxVisible);
  const overflowActions = hasOverflow ? visibleActions.slice(maxVisible - 1) : [];
  const displayedCount = directActions.length + (hasOverflow ? 1 : 0);

  if (!displayedCount) return null;

  const run = (action) => {
    setOverflowOpen(false);
    action.onClick();
  };

  return (
    <div class={`mconv-action-rail mconv-action-rail--${displayedCount}`}>
      {directActions.map((action) => <ActionButton key={action.id} action={action} onClick={() => run(action)} />)}
      {hasOverflow && (
        <>
          <button
            type="button"
            class={`mconv-action-rail-button${overflowOpen ? " is-active" : ""}`}
            aria-label="More session actions"
            aria-expanded={overflowOpen}
            onClick={() => setOverflowOpen((open) => !open)}
          >
            <MoreVertical size={18} aria-hidden="true" />
          </button>
          {overflowOpen && (
            <div class="mconv-action-rail-overflow" role="menu" aria-label="More session actions">
              {overflowActions.map((action) => <ActionButton key={action.id} action={action} menuItem onClick={() => run(action)} />)}
            </div>
          )}
        </>
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
