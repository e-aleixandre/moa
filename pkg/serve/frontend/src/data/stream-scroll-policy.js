export const AT_BOTTOM_PX = 80;

export function bottomScrollTop(scrollHeight, clientHeight) {
  return Math.max(0, scrollHeight - clientHeight);
}

export function isAtBottom(scrollTop, scrollHeight, clientHeight) {
  return bottomScrollTop(scrollHeight, clientHeight) - scrollTop < AT_BOTTOM_PX;
}

// ResizeObserver runs after content has grown, so inspect the scroll position
// against the previous height. That preserves the distinction between a reader
// who moved up and a follower whose content grew below the viewport.
export function scrollTopAfterContentResize(scrollTop, previousScrollHeight, scrollHeight, clientHeight) {
  if (!isAtBottom(scrollTop, previousScrollHeight, clientHeight)) return scrollTop;
  return bottomScrollTop(scrollHeight, clientHeight);
}
