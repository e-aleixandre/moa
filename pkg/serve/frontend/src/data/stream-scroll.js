import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { bottomScrollTop, followsTail, isAtBottom } from "./stream-scroll-policy.js";
import { loadOlderHistory, olderHistoryState } from "./history-paging.js";
import { capturePrependAnchor, nearTranscriptTop, restorePrependAnchor } from "./stream-prepend-anchor.js";

export function shouldLoadOlderHistory(el, paging, armed) {
  return armed && nearTranscriptTop(el) && paging.hasMore && !paging.loading;
}

export function capturePrependForSession(currentSessionId, sessionId, el) {
  if (currentSessionId !== sessionId) return null;
  return capturePrependAnchor(el);
}

// Shared transcript scroll intent for desktop and mobile. The content element
// is observed rather than the scroller: async image loads and expanding cards
// change its height, while the scroller's border box stays the same.
export function useStreamScroll({ session, sessionId, pendingAskId, followSignals, onScrollEl }) {
  const containerRef = useRef(null);
  const contentRef = useRef(null);
  const prependAnchor = useRef(null);
  const prependVersion = useRef(0);
  const olderHistoryArmed = useRef(true);
  const stickToBottom = useRef(true);
  const programmaticScroll = useRef(false);
  const lastScrollTop = useRef(0);
  const currentSessionId = useRef(sessionId);
  currentSessionId.current = sessionId;
  const [showNewBtn, setShowNewBtn] = useState(false);

  const scrollToBottomNow = useCallback(() => {
    const el = containerRef.current;
    if (!el) return;
    const target = bottomScrollTop(el.scrollHeight, el.clientHeight);
    if (el.scrollTop >= target) return;

    // Setting scrollTop cannot resize the observed content, but retain this
    // guard for a browser that delivers a nested layout notification here.
    programmaticScroll.current = true;
    el.scrollTop = target;
    // Read back the clamped value: a reader's move up from here must compare
    // against this pin, not against the scroll event of an earlier one.
    lastScrollTop.current = el.scrollTop;
    queueMicrotask(() => {
      programmaticScroll.current = false;
    });
  }, []);

  // Also consulted before every pin, because iOS can move scrollTop under a
  // momentum gesture before it delivers the scroll event.
  const followTail = useCallback((el) => {
    const following = followsTail(stickToBottom.current, lastScrollTop.current, el.scrollTop, el.scrollHeight, el.clientHeight);
    lastScrollTop.current = el.scrollTop;
    stickToBottom.current = following;
    setShowNewBtn(!following);
    return following;
  }, []);

  const checkScroll = useCallback(() => {
    const el = containerRef.current;
    if (!el) return;
    // Session switches and read anchors write scrollTop; those events must not
    // be read as the reader leaving the tail (iOS delivers them after the write).
    if (programmaticScroll.current) {
      lastScrollTop.current = el.scrollTop;
      return;
    }
    followTail(el);
    const paging = olderHistoryState(session);
    if (!nearTranscriptTop(el)) {
      olderHistoryArmed.current = true;
      return;
    }
    if (shouldLoadOlderHistory(el, paging, olderHistoryArmed.current)) {
      olderHistoryArmed.current = false;
      loadOlderHistory(sessionId, () => {
        const snapshot = capturePrependForSession(currentSessionId.current, sessionId, el);
        if (snapshot) prependAnchor.current = { ...snapshot, sessionId };
      });
    }
  }, [session, sessionId, followTail]);

  const setScrollEl = useCallback(
    (el) => {
      containerRef.current = el;
      if (onScrollEl) onScrollEl(el);
    },
    [onScrollEl]
  );

  // Position new streamed content before paint on both layouts. This also
  // avoids mobile briefly painting the previous session's scroll position.
  useLayoutEffect(() => {
    const el = containerRef.current;
    if (el && !programmaticScroll.current) followTail(el);
    if (stickToBottom.current) scrollToBottomNow();
  }, [scrollToBottomNow, followTail, ...followSignals]);

  useLayoutEffect(() => {
    stickToBottom.current = true;
    prependAnchor.current = null;
    prependVersion.current = 0;
    olderHistoryArmed.current = true;
    setShowNewBtn(false);
    const el = containerRef.current;
    programmaticScroll.current = true;
    if (el) {
      // Drop a leftover offset from the previous transcript, then pin to this
      // one's bottom. Both writes are programmatic so onScroll cannot unstick.
      el.scrollTop = 0;
      el.scrollTop = bottomScrollTop(el.scrollHeight, el.clientHeight);
      lastScrollTop.current = el.scrollTop;
    }
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        programmaticScroll.current = false;
      });
    });
  }, [sessionId]);

  // Restore the first visible durable block after a history prepend. This lives
  // beside the shared resize policy so desktop and mobile cannot diverge.
  useLayoutEffect(() => {
    const version = session?.olderHistory?.prependVersion || 0;
    if (version === prependVersion.current) return undefined;
    const el = containerRef.current;
    prependVersion.current = version;
    if (!el) return undefined;

    const snapshot = prependAnchor.current?.sessionId === sessionId ? prependAnchor.current : null;
    const node = restorePrependAnchor(el, snapshot, stickToBottom.current);
    lastScrollTop.current = el.scrollTop;
    if (!node || !snapshot || typeof globalThis.ResizeObserver === "undefined") return undefined;

    let expected = el.scrollTop;
    const observer = new globalThis.ResizeObserver(() => {
      // The ordinary resize observer only re-pins when following. Here the
      // reader deliberately loaded at the top, so retain their element anchor
      // unless they have scrolled since the restoration.
      if (stickToBottom.current || el.scrollTop !== expected) {
        observer.disconnect();
        return;
      }
      const offset = node.getBoundingClientRect().top - el.getBoundingClientRect().top;
      el.scrollTop += offset - snapshot.offset;
      expected = el.scrollTop;
      lastScrollTop.current = el.scrollTop;
    });
    observer.observe(contentRef.current || node);
    const timer = globalThis.setTimeout(() => observer.disconnect(), 1500);
    return () => {
      globalThis.clearTimeout(timer);
      observer.disconnect();
    };
  }, [sessionId, session?.olderHistory?.prependVersion, ...followSignals]);

  useLayoutEffect(() => {
    const content = contentRef.current;
    const el = containerRef.current;
    if (!content || !el || typeof globalThis.ResizeObserver === "undefined") return undefined;

    const observer = new globalThis.ResizeObserver(() => {
      const scroller = containerRef.current;
      if (!scroller || programmaticScroll.current) return;
      if (followTail(scroller)) scrollToBottomNow();
    });
    observer.observe(content);
    return () => observer.disconnect();
  }, [scrollToBottomNow, followTail]);

  useEffect(() => {
    if (!pendingAskId) return;
    stickToBottom.current = true;
    setShowNewBtn(false);
    scrollToBottomNow();
  }, [pendingAskId, scrollToBottomNow]);

  const scrollToBottom = useCallback(() => {
    stickToBottom.current = true;
    scrollToBottomNow();
    setShowNewBtn(false);
  }, [scrollToBottomNow]);

  const placeReadAnchor = useCallback((node, margin) => {
    const el = containerRef.current;
    if (!el || !node) return;
    programmaticScroll.current = true;
    el.scrollTop += node.getBoundingClientRect().top - el.getBoundingClientRect().top - margin;
    lastScrollTop.current = el.scrollTop;
    const following = isAtBottom(el.scrollTop, el.scrollHeight, el.clientHeight);
    stickToBottom.current = following;
    setShowNewBtn(!following);
    // Released after the frame, not after the microtask: the caller may be
    // placing a node that GREW in this same commit — an assignment the reader
    // just unfolded — and the resize observer runs later in the frame. Clearing
    // sooner let it read that growth as new tail content and pin the bottom
    // over the position placed here, dropping the reader past what they opened.
    const release = () => {
      programmaticScroll.current = false;
    };
    if (typeof globalThis.requestAnimationFrame === "function") {
      globalThis.requestAnimationFrame(() => globalThis.requestAnimationFrame(release));
    } else {
      queueMicrotask(release);
    }
  }, []);

  return { containerRef, contentRef, setScrollEl, checkScroll, scrollToBottom, placeReadAnchor, showNewBtn, stickToBottom };
}
