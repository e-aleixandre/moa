import { useRef } from "preact/hooks";
import { recallActivates } from "../../data/composer-queue.js";
import "./QueuedTail.css";

// QueuedTail — what you have already said, at the end of the thread.
//
// A queue is part of the conversation: the owner said those words and the
// agent will read them at its next step, so the receipt is visible where the
// conversation is, not as a note inside the box he is about to type in. But it
// is not another message either — it is a thread marker, the same grammar as a
// context-trim notice (.zl-sys), with one deliberately faint clipped trace per
// message so the owner can tell WHAT is waiting without unfolding anything.
//
// The whole marker row is the only gesture, 44px tall: pressing it brings the
// WHOLE queue back to the input. There is no per-message management — no ✕, no
// edit, no reorder, no disclosure (decisions/la-cola-no-se-gestiona.md): text
// is edited in the one place text is edited, the composer. The title names
// Alt+↑ because this row and that key are literally the same gesture, and the
// same action behind both (data/session-actions.js recallQueuedSteers).
export function QueuedTail({ queue, onBringBack }) {
  // Kept before the empty return: this component remains mounted as the queue
  // changes, so its hook order cannot depend on whether there are items.
  const armedPointerId = useRef(null);
  const items = (queue || []).filter(Boolean);
  if (items.length === 0) return null;
  const n = items.length;
  // Short on purpose: "N messages queued · …" wraps to a second line at 390px,
  // which is half the height of a marker whose whole premise is being small.
  const count = `${n} queued · read at the next step`;
  // A pointer activation only counts when its own pointerdown landed here.
  // This row is not born under the finger the way the composer's chip was (it
  // sits in the transcript, not where Send was just tapped), but a recall
  // destroys queued messages server-side, so the gesture stays whole-or-
  // nothing. Keyboard and assistive activations are always honoured — see
  // recallActivates. The arming is a ref, not a local: the transcript
  // re-renders on every streamed frame, and a local would be wiped between the
  // pointerdown and the click that completes the same tap.
  const activate = (opts) => {
    const armed = armedPointerId.current;
    armedPointerId.current = null;
    if (!recallActivates({ ...opts, armedPointerId: armed })) return;
    onBringBack?.();
  };
  return (
    <div class="zl-sys queued-tail">
      <button
        type="button"
        class="queued-line"
        onPointerDown={(e) => { armedPointerId.current = e.pointerId ?? true; }}
        onPointerCancel={() => { armedPointerId.current = null; }}
        onClick={(e) => activate({ pointerId: e.pointerId, detail: e.detail })}
        onKeyDown={(e) => {
          if (e.key !== "Enter" && e.key !== " ") return;
          e.preventDefault();
          activate({ fromKeyboard: true });
        }}
        title={`Bring back — the ${n === 1 ? "message goes" : "messages go"} back to the input (Alt+↑)`}
        aria-label={`${n} queued message${n === 1 ? "" : "s"} — bring ${n === 1 ? "it" : "them"} back to the input`}
      >
        <span class="queued-count zl-data">{count}</span>
        <span class="queued-spring" aria-hidden="true" />
        <span class="queued-act">Bring back</span>
      </button>
      {items.map((m) => (
        <div class={`queued-trace${m.command ? " is-command" : ""}`} key={m.id}>
          <span class="queued-prefix" aria-hidden="true">›</span>
          <span class="queued-text">{m.text}</span>
        </div>
      ))}
    </div>
  );
}
