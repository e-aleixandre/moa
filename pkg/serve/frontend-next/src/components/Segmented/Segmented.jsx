import { useRef } from "preact/hooks";
import "./Segmented.css";

function normalizeOptions(options) {
  return options.map((opt) => {
    if (typeof opt === "string") return { id: opt, label: opt };
    // Preserve any extra fields (e.g. ModelSelector's `bars`) so a caller's
    // renderOption can read them — only id/label are derived/defaulted here.
    return { ...opt, id: opt.value, label: opt.label ?? opt.value };
  });
}

// Segmented — generic segmented control (used for the thinking level
// in ModelSelector, but without coupling to those specific values). Implements
// the ARIA radiogroup pattern: roving tabIndex (only the selected item is
// tabbable) and Left/Right/Home/End arrows to change the option. `disabled`
// is optional (default false) — used by the session-settings popover to lock
// the permission-mode control while the agent is running, without changing
// the shape any existing caller (e.g. ModelSelector's thinking row) relies on.
//
// `className`/`itemClassName`/`renderOption` are optional escape hatches so a
// caller can restyle the chrome/cells (e.g. ModelSelector's thinking stepper,
// which needs a glyph-over-label cell instead of a plain text label) without
// forking the radiogroup/roving-tabindex/arrow-key logic above.
export function Segmented({
  options,
  value,
  onChange,
  disabled = false,
  className,
  itemClassName,
  renderOption,
  ...rest
}) {
  const items = normalizeOptions(options);
  const rootRef = useRef(null);

  const focusItem = (id) => {
    const el = rootRef.current?.querySelector(`[data-id="${CSS.escape(String(id))}"]`);
    el?.focus();
  };

  const onKeyDown = (event) => {
    if (disabled) return;
    let idx = items.findIndex((it) => it.id === value);
    if (idx === -1) idx = 0;
    let nextIdx = null;
    if (event.key === "ArrowRight" || event.key === "ArrowDown") {
      nextIdx = (idx + 1) % items.length;
    } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
      nextIdx = (idx - 1 + items.length) % items.length;
    } else if (event.key === "Home") {
      nextIdx = 0;
    } else if (event.key === "End") {
      nextIdx = items.length - 1;
    }
    if (nextIdx === null) return;
    event.preventDefault();
    const next = items[nextIdx];
    onChange?.(next.id);
    focusItem(next.id);
  };

  return (
    <div
      class={`segmented${className ? ` ${className}` : ""}`}
      role="radiogroup"
      aria-disabled={disabled || undefined}
      ref={rootRef}
      onKeyDown={onKeyDown}
      {...rest}
    >
      {items.map((opt) => {
        const on = opt.id === value;
        const isTabbable = value != null ? on : items[0]?.id === opt.id;
        return (
          <button
            key={opt.id}
            data-id={opt.id}
            type="button"
            class={`segmented-item${on ? " on" : ""}${itemClassName ? ` ${itemClassName(opt, on)}` : ""}`}
            role="radio"
            aria-checked={on}
            tabIndex={disabled ? -1 : (isTabbable ? 0 : -1)}
            disabled={disabled}
            onClick={() => !disabled && onChange?.(opt.id)}
          >
            {renderOption ? renderOption(opt, on) : opt.label}
          </button>
        );
      })}
    </div>
  );
}
