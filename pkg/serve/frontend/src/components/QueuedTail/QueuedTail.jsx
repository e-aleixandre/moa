import { useRef } from "preact/hooks";
import { queueRedirectState, recallActivates } from "../../data/composer-queue.js";
import { plainMessageText } from "../../data/util/mentions.js";
import "./QueuedTail.css";

// QueuedTail — what you have already said, at the end of the thread.
//
// A queue is part of the conversation: the owner said those words and the
// agent will read them when its current tool batch ends — a queued message
// cuts nothing — so the receipt is visible where the conversation is, not as a
// note inside the box he is about to type in. But it is not another message
// either — it is a thread marker, the same grammar as a context-trim notice
// (.zl-sys), with one deliberately faint clipped trace per message so the owner
// can tell WHAT is waiting without unfolding anything.
//
// The marker row carries at most two gestures, both on the WHOLE queue and each
// 44px tall:
//   - "Bring back" (the count and the label are two halves of one gesture):
//     the whole queue goes back to the input. There is no per-message
//     management — no ✕, no edit, no reorder, no disclosure
//     (decisions/la-cola-no-se-gestiona.md): text is edited in the one place
//     text is edited, the composer. The title names Alt+↑ because this row and
//     that key are literally the same gesture, and the same action behind both
//     (data/session-actions.js recallQueuedSteers).
//   - "Redirect now": the
//     urgent case — the agent reads the whole queue before its next step
//     instead of after the current one (redirectQueuedSteers). It is offered
//     only while there is something left to redirect; once the queue is marked
//     the count says "redirecting" in peach and the button is gone.
//
// Two buttons cannot be nested and "Redirect now" has to sit between the count
// and the "Bring back" label, so Bring back is two sibling buttons sharing one
// arming: the count side is the real control (the one with the label for
// assistive tech and the keyboard), the label side is a pointer-only twin kept
// out of the accessibility tree and the tab order.
export function QueuedTail({ queue, onBringBack, onRedirect }) {
  // Kept before the empty return: this component remains mounted as the queue
  // changes, so its hook order cannot depend on whether there are items.
  const armedPointerId = useRef(null);
  const armedRedirectPointerId = useRef(null);
  const items = (queue || []).filter(Boolean);
  if (items.length === 0) return null;
  const n = items.length;
  const { redirecting, canRedirect } = queueRedirectState(items);
  // Short on purpose: "N messages queued · …" wraps to a second line at 390px,
  // which is half the height of a marker whose whole premise is being small.
  // The tail is its own span so a narrow marker that also carries the Redirect
  // button can drop it (QueuedTail.css) instead of cutting the sentence.
  const head = `${n} queued`;
  const tail = redirecting ? " · redirecting" : " · read at the next step";
  // A pointer activation only counts when its own pointerdown landed here.
  // This row is not born under the finger the way the composer's chip was (it
  // sits in the transcript, not where Send was just tapped), but a recall
  // destroys queued messages server-side, so the gesture stays whole-or-
  // nothing. Keyboard and assistive activations are always honoured — see
  // recallActivates. The arming is a ref, not a local: the transcript
  // re-renders on every streamed frame, and a local would be wiped between the
  // pointerdown and the click that completes the same tap.
  const activate = (ref, action, opts) => {
    const armed = ref.current;
    ref.current = null;
    if (!recallActivates({ ...opts, armedPointerId: armed })) return;
    action?.();
  };
  const bringBack = (opts) => activate(armedPointerId, onBringBack, opts);
  const arm = (ref) => (e) => { ref.current = e.pointerId ?? true; };
  const disarm = (ref) => () => { ref.current = null; };
  const showRedirect = canRedirect;
  return (
    <div class="zl-sys queued-tail">
      <div class={`queued-row${showRedirect ? " has-redirect" : ""}`}>
        <button
          type="button"
          class="queued-line"
          onPointerDown={arm(armedPointerId)}
          onPointerCancel={disarm(armedPointerId)}
          onClick={(e) => bringBack({ pointerId: e.pointerId, detail: e.detail })}
          onKeyDown={(e) => {
            if (e.key !== "Enter" && e.key !== " ") return;
            e.preventDefault();
            bringBack({ fromKeyboard: true });
          }}
          title={`Bring back — the ${n === 1 ? "message goes" : "messages go"} back to the input (Alt+↑)`}
          aria-label={`${n} queued message${n === 1 ? "" : "s"} — bring ${n === 1 ? "it" : "them"} back to the input`}
        >
          <span class={`queued-count zl-data${redirecting ? " is-redirecting" : ""}`}>
            {head}
            <span class="queued-count-more">{tail}</span>
          </span>
          <span class="queued-spring" aria-hidden="true" />
        </button>
        {showRedirect && (
          <button
            type="button"
            class="queued-redirect"
            onPointerDown={arm(armedRedirectPointerId)}
            onPointerCancel={disarm(armedRedirectPointerId)}
            onClick={(e) => activate(armedRedirectPointerId, onRedirect, { pointerId: e.pointerId, detail: e.detail })}
            onKeyDown={(e) => {
              if (e.key !== "Enter" && e.key !== " ") return;
              e.preventDefault();
              activate(armedRedirectPointerId, onRedirect, { fromKeyboard: true });
            }}
            title="Redirect now — the agent reads the queue before its next step"
            aria-label="Redirect now — the agent reads the queue before its next step"
          >
            Redirect now
          </button>
        )}
        <button
          type="button"
          class="queued-bring queued-act"
          tabIndex={-1}
          aria-hidden="true"
          onPointerDown={arm(armedPointerId)}
          onPointerCancel={disarm(armedPointerId)}
          onClick={(e) => bringBack({ pointerId: e.pointerId, detail: e.detail })}
        >
          Bring back
        </button>
      </div>
      {items.map((m) => (
        <div class={`queued-trace${m.command ? " is-command" : ""}`} key={m.id}>
          <span class="queued-prefix" aria-hidden="true">›</span>
          <span class="queued-text">{plainMessageText(m.text)}</span>
        </div>
      ))}
    </div>
  );
}
