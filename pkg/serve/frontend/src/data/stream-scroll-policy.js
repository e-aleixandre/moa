// Sub-pixel slack only: a follower sits exactly at the bottom, because every
// pin and every clamp from shrinking content puts it there.
export const AT_BOTTOM_PX = 4;

export function bottomScrollTop(scrollHeight, clientHeight) {
  return Math.max(0, scrollHeight - clientHeight);
}

export function isAtBottom(scrollTop, scrollHeight, clientHeight) {
  return bottomScrollTop(scrollHeight, clientHeight) - scrollTop < AT_BOTTOM_PX;
}

// Whether the reader follows the tail after the scroller moved or its content
// changed size. Only the reader moves scrollTop up away from the bottom, so any
// upward move leaves the tail and only reaching the bottom rejoins it. Content
// growth leaves scrollTop alone and shrinking clamps it onto the bottom, so
// neither can flip the decision, however much the height changes.
export function followsTail(following, previousScrollTop, scrollTop, scrollHeight, clientHeight) {
  if (isAtBottom(scrollTop, scrollHeight, clientHeight)) return true;
  if (scrollTop < previousScrollTop) return false;
  return following;
}

// pendingPromptKey identifies what the session is waiting on inline (ask_user,
// secret request). A new key pins the stream to the tail once so the owner
// lands on the prompt; after that they can scroll up freely.
export function pendingPromptKey(session) {
  return [session?.pendingAsk?.id, session?.pendingSecrets?.[0]?.id].filter(Boolean).join("|");
}
