import { Plus } from "lucide-preact";
import { StateDot } from "../../../primitives/index.js";
import "./SessionStrip.css";

// SessionStrip — scrollable horizontal strip of session chips under the
// mobile header. Each chip has its state dot + name; the active one is
// highlighted in peach, the one needing attention in yellow with an unseen dot.
// Last "+" chip to create a session. Horizontal scroll hides the scrollbar.
export function SessionStrip({ sessions = [], activeId, onSelect, onNew }) {
  return (
    <div class="mstrip" tabIndex={0} role="group" aria-label="Sessions">
      {sessions.map((s) => {
        const active = s.id === activeId;
        const cls = `mstrip-chip${active ? " on" : ""}${s.needs ? " needs" : ""}`;
        return (
          <button
            key={s.id}
            type="button"
            class={cls}
            aria-current={active ? "true" : undefined}
            title={s.name}
            onClick={() => onSelect && onSelect(s.id)}
          >
            <StateDot state={s.state} size={6} />
            <span class="mstrip-name">{s.name}</span>
            {s.unseen && <span class="mstrip-unseen" aria-hidden="true" />}
          </button>
        );
      })}
      <button
        type="button"
        class="mstrip-chip plus"
        aria-label="New session"
        onClick={onNew}
      >
        <Plus size={13} aria-hidden="true" />
      </button>
    </div>
  );
}
