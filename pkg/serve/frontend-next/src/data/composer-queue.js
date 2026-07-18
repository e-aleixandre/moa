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
