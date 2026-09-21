import { PhoneOff, PhoneCall, X, Pencil } from "lucide-preact";
import { Sheet } from "../Sheet/Sheet.jsx";
import { callClock, formatCallCost, CALL_COST_TITLE } from "../../data/design-variant.js";
import "./DesignProposals.css";

// DesignProposals — PROPOSAL CODE. Not product. Every piece here is mounted
// only from a branch guarded by data/design-variant.js, which is inert unless
// the URL carries ?cq= / ?cqcase=. Kept in one file so the proposal can be
// deleted in one movement once the owner has chosen.

// --- A -------------------------------------------------------------------
// A queued message painted as what it already is: a message. Same grammar as
// UserWaypoint (position, type, margins), in a "said but not landed" face:
// no fill, a dotted hairline, and only its own ✕ in the foot.
//
// The state is said ONCE, as a hairline label over the block: repeating
// "queued · not read yet" under every cell turned three messages into three
// identical lines of noise, and the noise was louder than the idea.

export function QueuedWaypoint({ message, onCancel }) {
  const isCommand = !!message.command;
  return (
    <div class="zl-user dp-queued">
      <div class="zl-user-cell">
        <div class={`zl-user-body${isCommand ? " dp-queued-cmd" : ""}`}>{message.text}</div>
      </div>
      <div class="zl-user-foot">
        <button
          type="button"
          class="dp-queued-x"
          aria-label="Cancel this queued message"
          title="Cancel this message — it goes back to the composer"
          onClick={() => onCancel?.(message.id)}
        >
          <X size={13} aria-hidden="true" />
        </button>
      </div>
    </div>
  );
}

export function QueuedTail({ queue, onCancel }) {
  if (!queue?.length) return null;
  const n = queue.length;
  return (
    <div class="dp-queued-tail">
      <div class="dp-queued-group zl-data">
        {n} queued · read at the next step
      </div>
      {queue.map((m) => <QueuedWaypoint key={m.id} message={m} onCancel={onCancel} />)}
    </div>
  );
}

// CallLine — A's call: one flat row inside the composer's slab, with no fill
// and no rim of its own, so the call stops being a plane inside a plane. The
// mic line and the "waiting" notice are conditional: in the normal state a
// call is ONE line, not three. Also used by C once the owner asks to type.
export function CallLine({ call, onHangup }) {
  const cost = formatCallCost(call.costUSD);
  const micBad = call.micState && call.micState !== "live";
  return (
    <div class="dp-callline-wrap">
      <div class="dp-callline" role="status" aria-live="polite">
        <span class="dp-call-dot" aria-hidden="true" />
        <span class="dp-call-phase">On a call</span>
        <span class="dp-call-clock zl-data">{callClock(call.elapsed)}</span>
        <span class="dp-call-spring" />
        {cost && <span class="dp-call-cost zl-data" title={CALL_COST_TITLE}>{cost}</span>}
        <span class="dp-call-q zl-data" title="Questions the delegate may ask this conversation">
          {call.questionsUsed}/{call.maxQuestions}
        </span>
        <button
          type="button"
          class="dp-call-hangup"
          aria-label="End call"
          title="End call — the minutes land in the composer"
          onClick={onHangup}
        >
          <PhoneOff size={16} aria-hidden="true" />
        </button>
      </div>
      {call.pendingAsks > 0 && (
        <div class="dp-call-waiting">Waiting for this conversation to answer the delegate…</div>
      )}
      {micBad && (
        <div class={`dp-call-mic is-${call.micState === "not-live" ? "bad" : "warn"}`}>
          {call.micState === "not-live"
            ? "Mic not live — it cannot hear you"
            : "Mic paused while this tab is in the background"}
        </div>
      )}
    </div>
  );
}

// --- B -------------------------------------------------------------------
// The queue as an item of the LiveBar's tally, and the LiveBar's own panel as
// its list.

export function QueuePill({ count, open, onToggle }) {
  return (
    <button
      type="button"
      class="zl-live-tally dp-queue-pill"
      onClick={onToggle}
      aria-expanded={open}
      aria-label={`${count} queued message${count === 1 ? "" : "s"}${open ? ", collapse" : ", expand"}`}
    >
      <span class="dp-queue-mark" aria-hidden="true" />
      <span class="zl-live-n zl-data">{count}</span>
      <span class="dp-queue-word">queued</span>
    </button>
  );
}

export function QueueRows({ queue, onCancel, onEdit }) {
  return (
    <div class="zl-live-grp dp-queue-rows">
      <div class="zl-group">
        <span>Queued for the agent</span>
        <span class="zl-group-n zl-data">{queue.length}</span>
      </div>
      {queue.map((m) => (
        <div class="dp-queue-row" key={m.id}>
          <span class={`dp-queue-row-t${m.command ? " zl-data" : ""}`}>{m.text}</span>
          <button type="button" class="dp-queue-edit" onClick={() => onEdit?.(m.id)} title="Edit — takes it out of the queue and back to the composer">
            Edit
          </button>
          <button type="button" class="dp-queue-x" aria-label="Cancel this queued message" title="Cancel this message" onClick={() => onCancel?.(m.id)}>
            <X size={13} aria-hidden="true" />
          </button>
        </div>
      ))}
    </div>
  );
}

// --- C -------------------------------------------------------------------
// The queue as sheets stacked behind the slab's top edge plus a discreet
// counter on the rim. The slab does not grow: the cost is ~10px of paper.

export function QueueStack({ count, onOpen }) {
  const leaves = Math.min(count, 2);
  return (
    <>
      <div class="dp-stack" aria-hidden="true">
        {Array.from({ length: leaves }, (_, i) => <span class={`dp-stack-leaf is-${i + 1}`} key={i} />)}
      </div>
      {/* A tab seated ON the slab's top edge, in the paper's own language: a
          bare number floating in the corner read as a notification badge
          stuck to the object rather than as part of it. */}
      <button
        type="button"
        class="dp-stack-tab"
        onClick={onOpen}
        aria-label={`${count} queued message${count === 1 ? "" : "s"} — open the list`}
        title="Queued messages"
      >
        <span class="dp-stack-tab-n zl-data">{count}</span>
        <span class="dp-stack-tab-w">queued</span>
      </button>
    </>
  );
}

export function QueueSheet({ open, queue, onClose, onCancel, onEdit }) {
  return (
    <Sheet open={open} onClose={onClose} title="Queued messages" ariaLabel="Queued messages" class="dp-queue-sheet">
      <p class="dp-queue-sheet-lead">Delivered to the agent — it reads them at its next step.</p>
      {(queue || []).map((m) => (
        <div class="dp-sheet-row" key={m.id}>
          <span class={`dp-sheet-row-t${m.command ? " dp-mono" : ""}`}>{m.text}</span>
          <button type="button" class="dp-sheet-edit" onClick={() => onEdit?.(m.id)} title="Edit — back to the composer">
            <Pencil size={13} aria-hidden="true" /> Edit
          </button>
          <button type="button" class="dp-sheet-x" aria-label="Cancel this queued message" onClick={() => onCancel?.(m.id)}>
            <X size={14} aria-hidden="true" />
          </button>
        </div>
      ))}
    </Sheet>
  );
}

// CallFace — C's composer while a call runs: the slab keeps its size and
// changes face. The product stops pretending you are typing.
export function CallFace({ call, onHangup, onTypeInstead }) {
  const cost = formatCallCost(call.costUSD);
  const micLabel = call.micState === "live"
    ? "Mic live"
    : call.micState === "not-live"
      ? "Mic not live — it cannot hear you"
      : "Mic paused while this tab is in the background";
  const micTone = call.micState === "live" ? "ok" : call.micState === "not-live" ? "bad" : "warn";
  return (
    <div class="dp-callface" role="status" aria-live="polite">
      <div class="dp-callface-id">
        <span class="dp-callface-glyph" aria-hidden="true"><PhoneCall size={18} /></span>
        <span class="dp-callface-lines">
          <span class="dp-callface-title">
            On a call <span class="dp-call-clock zl-data">{callClock(call.elapsed)}</span>
          </span>
          <span class={`dp-callface-mic is-${micTone}`}>{micLabel}</span>
          {call.pendingAsks > 0 && (
            <span class="dp-callface-waiting">Waiting for this conversation to answer the delegate…</span>
          )}
        </span>
      </div>
      <div class="dp-callface-right">
        <span class="dp-callface-meta zl-data">
          {cost && <span title={CALL_COST_TITLE}>{cost}</span>}
          {cost && " · "}
          {call.questionsUsed}/{call.maxQuestions} questions
        </span>
        <div class="dp-callface-acts">
          <button type="button" class="dp-callface-type" onClick={onTypeInstead}>Type instead</button>
          <button type="button" class="dp-callface-hangup" onClick={onHangup} aria-label="End call" title="End call — the minutes land in the composer">
            <PhoneOff size={17} aria-hidden="true" />
          </button>
        </div>
      </div>
    </div>
  );
}
