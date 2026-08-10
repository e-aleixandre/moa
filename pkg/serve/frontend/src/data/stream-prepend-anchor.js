// stream-prepend-anchor.js — element anchoring for backward transcript pages

function anchors(el) { return [...el.querySelectorAll('[data-stream-anchor]')]; }

const FLICK_SCROLL_DELTA = 24;

// The leading "Older messages…" marker is transcript chrome, not history: it is
// re-rendered above whichever page arrives next. Anchoring to it keeps the
// reader pinned to the top, so the page lands below them instead of above.
const CHROME_ANCHOR_IDS = new Set(['sys-truncated']);

function isHistoryAnchor(node) {
  return !CHROME_ANCHOR_IDS.has(node.dataset.streamAnchor);
}

// The first history block is the seam: an incoming page can continue the same
// document, so projection re-keys that block to the earlier message it now
// starts from and its identity does not survive the prepend. Every later block
// does, so anchor to the one after the seam.
function anchorNode(el) {
  const history = anchors(el).filter(isHistoryAnchor);
  return history[1] || history[0];
}

export function capturePrependAnchor(el) {
  const top = el.getBoundingClientRect().top;
  const node = anchorNode(el);
  return {
    id: node?.dataset.streamAnchor || '',
    offset: node ? node.getBoundingClientRect().top - top : 0,
    scrollTop: el.scrollTop,
  };
}

// Do not override a scroll that changed since capture: a flick is user intent.
// Returning the node lets the caller keep it anchored while newly inserted
// images settle their intrinsic dimensions.
export function restorePrependAnchor(el, snapshot, stickToBottom) {
  if (!snapshot || stickToBottom) return null;
  const node = anchors(el).find(item => item.dataset.streamAnchor === snapshot.id);
  // The captured block may have been replaced by the authoritative history
  // update. Keep the old position explicitly rather than letting a resize
  // leave the reader displaced by the entire prepended page.
  if (!node) {
    el.scrollTop = snapshot.scrollTop;
    return null;
  }
  // Small momentum adjustments are not a new reading intent. A substantial
  // movement is a deliberate flick, which must win over the pending restore.
  if (Math.abs(el.scrollTop - snapshot.scrollTop) > FLICK_SCROLL_DELTA) return null;
  el.scrollTop += node.getBoundingClientRect().top - el.getBoundingClientRect().top - snapshot.offset;
  return node;
}

export function nearTranscriptTop(el, threshold = 96) {
  return !!el && el.scrollTop <= threshold;
}
