// composer-queue.js — pure helpers for the composer's queue/steer semantics.
//
// The Composer (src/layout/Composer/Composer.jsx) carries a lot of DOM-bound
// state (textarea ref, history, drafts) that isn't worth unit-testing, but the
// two delicate, race-prone decisions — "does this message get sent now or
// enqueued as a steer?" and "how does the queue collapse back into the input on
// recall/abort?" — are extracted here as pure functions so they can be tested
// in isolation. These mirror the logic ported from the old SPA's InputBar.jsx
// (handleSendInner's idle check, handleDequeueSteers/handleStop's queue dump)
// and session-actions' sendMessage (the isIdle branch that mints a steer chip).

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

// queueSummary condenses the queue into what the composer chip renders: the
// total count plus the last chip's text (the most recent intent), matching the
// old InputBar's "last message + N queued" chip. Returns null for an empty
// queue so the caller can hide the chip entirely.
export function queueSummary(pendingSteers) {
  const steers = (pendingSteers || []).filter(Boolean);
  if (steers.length === 0) return null;
  const last = steers[steers.length - 1];
  return {
    count: steers.length,
    lastText: last.command ? last.text.replace(/^\//, '') : last.text,
    lastIsCommand: !!last.command,
    lastImages: last.images || 0,
  };
}

// recallActivates decides whether a click on the "N queued" chip is a real
// recall or an inherited one.
//
// The chip is born under the finger: it renders the instant a message is
// queued, in the composer, right where the send button was just tapped. The
// click that completes that tap is then delivered to whatever occupies the
// point — the chip that did not exist when the gesture began. Because a recall
// cancels the queued messages server-side, that stray click destroyed the
// message the user had just written.
//
// A gesture only counts when its pointerdown landed on the chip too. That is a
// property of the gesture, not of the clock, so it needs no grace period and
// never penalises a genuinely fast tap. Keyboard activation (Alt+↑, Enter on a
// focused chip) has no pointer at all and is always honoured.
export function recallActivates({ pointerDownOnChip, fromKeyboard }) {
  if (fromKeyboard) return true;
  return pointerDownOnChip === true;
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
