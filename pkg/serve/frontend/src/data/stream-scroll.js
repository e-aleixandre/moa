import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import {
  bottomScrollTop,
  isAtBottom,
  scrollTopAfterContentResize,
} from "./stream-scroll-policy.js";

// Shared transcript scroll intent for desktop and mobile. The content element
// is observed rather than the scroller: async image loads and expanding cards
// change its height, while the scroller's border box stays the same.
export function useStreamScroll({ sessionId, pendingAskId, followSignals, onScrollEl }) {
  const containerRef = useRef(null);
  const contentRef = useRef(null);
  const stickToBottom = useRef(true);
  const programmaticScroll = useRef(false);
  const observedScrollHeight = useRef(0);
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
    if (!el) return;
    const atBottom = isAtBottom(el.scrollTop, el.scrollHeight, el.clientHeight);
    stickToBottom.current = atBottom;
    setShowNewBtn(!atBottom);
  }, []);

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
    setShowNewBtn(false);
    scrollToBottomNow();
  }, [sessionId, scrollToBottomNow]);

  useLayoutEffect(() => {
    const content = contentRef.current;
    const el = containerRef.current;
    if (!content || !el || typeof ResizeObserver === "undefined") return undefined;

    observedScrollHeight.current = el.scrollHeight;

    const observer = new ResizeObserver(() => {
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

  return { containerRef, contentRef, setScrollEl, checkScroll, scrollToBottom, showNewBtn, stickToBottom };
}
