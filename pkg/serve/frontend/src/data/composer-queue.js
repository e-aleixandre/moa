// composer-queue.js — pure helpers for the composer's queue/steer semantics.
//
// The Composer (src/layout/Composer/Composer.jsx) carries a lot of DOM-bound
// state (textarea ref, history, drafts) that isn't worth unit-testing, but the
// two delicate, race-prone decisions — "does this message get sent now or
// enqueued as a steer?" and "how does the queue collapse back into the input on
// recall/abort?" — are extracted here as pure functions so they can be tested
// in isolation. These mirror the logic ported from the old SPA's InputBar.jsx
// (handleSendInner's idle check and queue recall/abort text merge) and
// session-actions' sendMessage (the isIdle branch that mints a steer chip).

// willEnqueue decides whether a normal message will be sent immediately (starts
// a run) or enqueued as a steer chip. Mirrors sendMessage's `isIdle` gate: an
// idle or errored session runs the message now; anything else (running,
// permission, …) queues it. A missing session sends nothing, so it can't queue.
export function willEnqueue(session) {
  if (!session) return false;
  const state = session.state;
  return !(state === 'idle' || state === 'error');
}

// combineQueueText merges the queued steer texts back into the textarea value
// on recall (Alt+↑ / chip click) or abort (Esc). Ported verbatim from
// InputBar.handleDequeueSteers / handleStop: the chips are joined with newlines
// and appended after the current draft (with a separating newline when the
// draft is non-empty). Command chips carry their full "/command" text and
// message chips their text, so a plain `.text` join is faithful.
export function combineQueueText(currentValue, pendingSteers) {
  const steers = (pendingSteers || []).filter(Boolean);
  const combined = steers.map((s) => s.text).join('\n');
  const current = currentValue || '';
  if (!combined) return current;
  return current ? current + '\n' + combined : combined;
}

// droppedImageCount sums the images queued across all chips. Queued images
// can't be pulled back into the input (their base64 was never tracked
// client-side, only the count), so recall/abort warn with this number. Mirrors
// InputBar's `reduce((n, s) => n + (s.images || 0), 0)`.
export function droppedImageCount(pendingSteers) {
  return (pendingSteers || []).reduce((n, s) => n + (s.images || 0), 0);
}

// recallActivates decides whether a click on a queue recall control is a real
// recall or an inherited one.
//
// A recall cancels queued messages server-side, so an inherited click cannot
// count: the control must receive the pointerdown belonging to its click. The
// pointer ID makes that a property of one gesture rather than a timing window.
// Pointerleave intentionally does not disarm it — touch taps leave before their
// click — while pointercancel still does. Keyboard and assistive activations
// have no pointer and are always honoured.
export function recallActivates({ armedPointerId, pointerId, detail, fromKeyboard }) {
  if (fromKeyboard) return true;
  if (detail === 0) return true; // synthesised activation (screen reader, .click())
  if (armedPointerId == null || armedPointerId === false) return false;
  if (armedPointerId === true) return true; // no pointerId available (older engines)
  return armedPointerId === pointerId;
}

// sendMayClear decides whether a send that has just been accepted is still
// entitled to empty the composer.
//
// An ordinary message waits for the server before clearing, so a rejected send
// leaves the text there to retry. That wait is also a window in which the box
// can legitimately acquire text this send never owned: a queue recall restoring
// the messages it cancels, or an abort dumping them back. Clearing blindly then
// destroys text the user never sent — the queued message vanishes from the
// server AND the screen at once.
//
// Comparing the text is not enough: a recall restores the very message that was
// just sent, so the two are equal by value. Only a counter bumped by every
// foreign write distinguishes them.
export function sendMayClear(sendEpoch, currentEpoch) {
  return sendEpoch === currentEpoch;
}
