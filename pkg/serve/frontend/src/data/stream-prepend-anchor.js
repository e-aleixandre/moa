// stream-prepend-anchor.js — element anchoring for backward transcript pages

import { isStableBlockID } from './stream-model.js';

function anchors(el) { return [...el.querySelectorAll('[data-stream-anchor]')]; }

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
// does — unless its ID is positional rather than identity-based, which a
// prepend shifts just as surely. Skip the seam, then take the first block whose
// identity actually survives the incoming page.
function anchorNode(el) {
  const history = anchors(el).filter(isHistoryAnchor);
  const stable = history.slice(1).find(node => isStableBlockID(node.dataset.streamAnchor));
  return stable || history[1] || history[0];
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
  el.scrollTop += node.getBoundingClientRect().top - el.getBoundingClientRect().top - snapshot.offset;
  return node;
}

export function nearTranscriptTop(el, threshold = 96) {
  return !!el && el.scrollTop <= threshold;
}
