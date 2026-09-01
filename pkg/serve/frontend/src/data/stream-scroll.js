import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import {
  bottomScrollTop,
  isAtBottom,
  scrollTopAfterContentResize,
} from "./stream-scroll-policy.js";
import { loadOlderHistory, olderHistoryState } from "./history-paging.js";
import { capturePrependAnchor, nearTranscriptTop, restorePrependAnchor } from "./stream-prepend-anchor.js";

export function shouldLoadOlderHistory(el, paging, armed) {
  return armed && nearTranscriptTop(el) && paging.hasMore && !paging.loading;
}

export function restorePrependLayout(el, snapshot, stickToBottom, observedScrollHeight) {
  const node = restorePrependAnchor(el, snapshot, stickToBottom);
  // A prepend has already restored its reader position. Make its new height
  // the baseline so the ordinary resize observer cannot treat it as tail growth.
  observedScrollHeight.current = el.scrollHeight;
  return node;
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
  const observedScrollHeight = useRef(0);
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
    queueMicrotask(() => {
      programmaticScroll.current = false;
    });
  }, []);

  const checkScroll = useCallback(() => {
    const el = containerRef.current;
    // Session switches and follow-pins write scrollTop; those events must not
    // be read as the reader leaving the tail (iOS delivers them after the write).
    if (!el || programmaticScroll.current) return;
    const atBottom = isAtBottom(el.scrollTop, el.scrollHeight, el.clientHeight);
    stickToBottom.current = atBottom;
    setShowNewBtn(!atBottom);
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
  }, [session, sessionId]);

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
    if (stickToBottom.current) scrollToBottomNow();
  }, [scrollToBottomNow, ...followSignals]);

  useLayoutEffect(() => {
    stickToBottom.current = true;
    prependAnchor.current = null;
    prependVersion.current = 0;
    olderHistoryArmed.current = true;
    observedScrollHeight.current = 0;
    setShowNewBtn(false);
    const el = containerRef.current;
    programmaticScroll.current = true;
    if (el) {
      // Drop a leftover offset from the previous transcript, then pin to this
      // one's bottom. Both writes are programmatic so onScroll cannot unstick.
      el.scrollTop = 0;
      el.scrollTop = bottomScrollTop(el.scrollHeight, el.clientHeight);
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
    const node = restorePrependLayout(el, snapshot, stickToBottom.current, observedScrollHeight);
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

    observedScrollHeight.current = el.scrollHeight;

    const observer = new globalThis.ResizeObserver(() => {
      const scroller = containerRef.current;
      if (!scroller) return;

      const previousScrollHeight = observedScrollHeight.current;
      observedScrollHeight.current = scroller.scrollHeight;
      if (programmaticScroll.current) return;

      const nextScrollTop = scrollTopAfterContentResize(
        scroller.scrollTop,
        previousScrollHeight,
        scroller.scrollHeight,
        scroller.clientHeight
      );
      const following = nextScrollTop !== scroller.scrollTop || isAtBottom(
        scroller.scrollTop,
        previousScrollHeight,
        scroller.clientHeight
      );
      stickToBottom.current = following;
      setShowNewBtn(!following);
      if (!following) return;
      scrollToBottomNow();
    });
    observer.observe(content);
    return () => observer.disconnect();
  }, [scrollToBottomNow]);

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
    const following = isAtBottom(el.scrollTop, el.scrollHeight, el.clientHeight);
    stickToBottom.current = following;
    setShowNewBtn(!following);
    queueMicrotask(() => {
      programmaticScroll.current = false;
    });
  }, []);

  return { containerRef, contentRef, setScrollEl, checkScroll, scrollToBottom, placeReadAnchor, showNewBtn, stickToBottom };
}
