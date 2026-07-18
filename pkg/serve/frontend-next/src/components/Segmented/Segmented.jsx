import { useRef } from "preact/hooks";
import "./Segmented.css";

function normalizeOptions(options) {
  return options.map((opt) => {
    if (typeof opt === "string") return { id: opt, label: opt };
    return { id: opt.value, label: opt.label ?? opt.value };
  });
}

// Segmented — control segmentado genérico (usado para el nivel de thinking
// en ModelSelector, pero sin acoplarse a esos valores concretos). Implementa
// el patrón ARIA radiogroup: roving tabIndex (solo el seleccionado es
// tabbable) y flechas Izquierda/Derecha/Home/End para cambiar de opción.
export function Segmented({ options, value, onChange, ...rest }) {
  const items = normalizeOptions(options);
  const rootRef = useRef(null);

  const focusItem = (id) => {
    const el = rootRef.current?.querySelector(`[data-id="${CSS.escape(String(id))}"]`);
    el?.focus();
  };

  const onKeyDown = (event) => {
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
    <div class="segmented" role="radiogroup" ref={rootRef} onKeyDown={onKeyDown} {...rest}>
      {items.map((opt) => {
        const on = opt.id === value;
        const isTabbable = value != null ? on : items[0]?.id === opt.id;
        return (
          <button
            key={opt.id}
            data-id={opt.id}
            type="button"
            class={`segmented-item${on ? " on" : ""}`}
            role="radio"
            aria-checked={on}
            tabIndex={isTabbable ? 0 : -1}
            onClick={() => onChange?.(opt.id)}
          >
            {opt.label}
          </button>
        );
      })}
    </div>
  );
}
