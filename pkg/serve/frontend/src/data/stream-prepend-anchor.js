// stream-prepend-anchor.js — element anchoring for backward transcript pages

function anchors(el) { return [...el.querySelectorAll('[data-stream-anchor]')]; }

const FLICK_SCROLL_DELTA = 24;

export function capturePrependAnchor(el) {
  const top = el.getBoundingClientRect().top;
  // The first durable block is the boundary between the existing transcript
  // and the page being inserted. Keeping that boundary anchored means a page
  // loaded at the top is added above the reader, rather than under them.
  const node = anchors(el)[0];
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
