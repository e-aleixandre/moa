// stream-hydration-anchor.js — retain a reader's place across a history swap

function anchorsIn(el) {
  return [...el.querySelectorAll("[data-stream-anchor]")];
}

export function captureHydrationAnchor(el, sessionId, historyPending) {
  const top = el.getBoundingClientRect().top;
  const anchor = anchorsIn(el).find((node) => node.getBoundingClientRect().bottom > top);
  return {
    sessionId,
    historyPending: !!historyPending,
    id: anchor?.dataset.streamAnchor,
    offset: anchor ? anchor.getBoundingClientRect().top - top : 0,
    scrollTop: el.scrollTop,
  };
}

// Returns whether a pending hydration transition was restored. The snapshot is
// taken after each committed render; at the following layout effect its block
// identity lets us preserve the reader's exact viewport offset through init.
export function restoreHydrationAnchor(el, previous, sessionId, historyPending, stickToBottom) {
  if (
    previous?.sessionId !== sessionId ||
    !previous.historyPending ||
    historyPending ||
    stickToBottom
  ) return false;

  const anchor = anchorsIn(el).find((node) => node.dataset.streamAnchor === previous.id);
  if (anchor) {
    const offset = anchor.getBoundingClientRect().top - el.getBoundingClientRect().top;
    el.scrollTop += offset - previous.offset;
  } else {
    el.scrollTop = previous.scrollTop;
  }
  return true;
}
