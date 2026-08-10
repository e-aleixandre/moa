export const AT_BOTTOM_PX = 80;

export function bottomScrollTop(scrollHeight, clientHeight) {
  return Math.max(0, scrollHeight - clientHeight);
}

export function isAtBottom(scrollTop, scrollHeight, clientHeight) {
  return bottomScrollTop(scrollHeight, clientHeight) - scrollTop < AT_BOTTOM_PX;
}

export function shouldRepinAfterContentResize(stickToBottom) {
  return stickToBottom;
}

export function shouldPinForSessionChange() {
  return true;
}
