// stream-read-anchor.js — one-shot positioning for an unread completed result

export const READ_ANCHOR_MARGIN = 12;

const pendingSessionIDs = new Set();

function completed(session) {
  return session?.state === 'idle';
}

function completedUnread(session) {
  return !!session?.unseen && completed(session);
}

function lastMessageIsUser(session) {
  const messages = session?.messages || [];
  for (let i = messages.length - 1; i >= 0; i--) {
    const message = messages[i];
    if (!message || message._type === 'system') continue;
    return message.role === 'user';
  }
  return false;
}

function hasAssistantMessage(session) {
  return (session?.messages || []).some((message) => message?.role === 'assistant');
}

// Prefer the last prose document: this is the reply the reader came back to
// read. A turn which finishes on a tool has no final prose, so fall back to
// that turn's last rendered assistant document rather than stranding them at
// the transcript tail.
export function readAnchorBlockID(session, blocks = []) {
  if (!completed(session) || lastMessageIsUser(session) || !hasAssistantMessage(session)) return null;

  const messages = session.messages || [];
  const lastMessage = messages[messages.length - 1];
  // A completed tool with no prose afterwards is the useful terminal record;
  // do not jump back to a provisional sentence that preceded the tool.
  if (lastMessage?._type === 'tool_start') {
    for (let i = blocks.length - 1; i >= 0; i--) {
      if (blocks[i]?.kind === 'document' || blocks[i]?.kind === 'streaming') return blocks[i].id;
    }
    return null;
  }

  let fallback = null;
  for (let i = blocks.length - 1; i >= 0; i--) {
    const block = blocks[i];
    if (block?.kind !== 'document' && block?.kind !== 'streaming') continue;
    fallback ||= block.id;
    if (block.blocks?.some((item) => item.type === 'prose')) return block.id;
  }
  return fallback;
}

export function armReadAnchor(session) {
  if (!completedUnread(session)) return false;
  pendingSessionIDs.add(session.id);
  return true;
}

export function hasReadAnchor(sessionID) {
  return pendingSessionIDs.has(sessionID);
}

export function consumeReadAnchor(sessionID) {
  if (!pendingSessionIDs.has(sessionID)) return false;
  pendingSessionIDs.delete(sessionID);
  return true;
}

// Cards can settle after their initial paint. Keep the selected block at its
// offset briefly, but stop immediately if the reader scrolls.
export function settleReadAnchor(el, content, node, reposition) {
  if (!el || !content || !node || typeof globalThis.ResizeObserver === 'undefined') return () => {};
  let expected = el.scrollTop;
  const observer = new globalThis.ResizeObserver(() => {
    if (el.scrollTop !== expected) {
      observer.disconnect();
      return;
    }
    reposition(node);
    expected = el.scrollTop;
  });
  observer.observe(content);
  const timer = globalThis.setTimeout(() => observer.disconnect(), 1500);
  return () => {
    globalThis.clearTimeout(timer);
    observer.disconnect();
  };
}

export function __resetReadAnchorsForTests() {
  pendingSessionIDs.clear();
}
