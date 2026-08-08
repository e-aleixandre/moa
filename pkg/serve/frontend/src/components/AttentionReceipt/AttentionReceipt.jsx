import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import {
  conversationVisibilityKey,
  store,
  updateSession,
} from "../../data/store.js";
import {
  acknowledgeRenderedPendingAttention,
  refreshAttentionInstances,
  StaleServerInstanceError,
} from "../../data/api.js";
import { forgetPendingAttention, rememberedPendingAttention, rememberPendingAttention } from "../../data/attention-receipt-store.js";
import "./AttentionReceipt.css";

function occurrenceKey(pending) {
  if (!pending) return "";
  const generation = pending.unseen_gen ?? pending.unseenGen ?? 0;
  const instance = pending.server_instance ?? pending.serverInstance ?? "";
  return `${pending.id || ""}:${generation}:${instance}`;
}

// A fixed visible region is reachable even for an arbitrarily tall prompt.
// It is deliberately modest: seeing this much of a mounted request in the
// foreground is stronger than a one-pixel edge overlap, without making a
// four-viewport card impossible to acknowledge.
export const PROMPT_RECEIPT_MIN_VISIBLE_PX = 48;
export const PROMPT_RECEIPT_VIEWPORT_FRACTION = 0.1;
export const RECEIPT_RETRY_INITIAL_MS = 500;
export const RECEIPT_RETRY_MAX_MS = 16000;

export function isMeaningfullyIntersecting(entry) {
  if (!entry?.isIntersecting) return false;
  const visible = entry.intersectionRect;
  const bounds = entry.boundingClientRect;
  const root = entry.rootBounds;
  if (!visible || !bounds) return false;
  const viewportHeight = root?.height || (typeof window !== "undefined" ? window.innerHeight : 0) || 0;
  const viewportWidth = root?.width || (typeof window !== "undefined" ? window.innerWidth : 0) || 0;
  // IntersectionObserver uses the layout viewport on iOS, which can include
  // the region covered by the software keyboard. Its rectangle coordinates are
  // available in real observer entries, so additionally intersect them with
  // visualViewport when present. Minimal test doubles without coordinates keep
  // the ordinary observer threshold behavior.
  const visual = typeof window !== "undefined" ? window.visualViewport : null;
  if (visual?.width > 0 && visual?.height > 0 && Number.isFinite(visible.top) && Number.isFinite(visible.left)) {
    const top = Math.max(visible.top, visual.offsetTop || 0);
    const bottom = Math.min(visible.bottom, (visual.offsetTop || 0) + visual.height);
    const left = Math.max(visible.left, visual.offsetLeft || 0);
    const right = Math.min(visible.right, (visual.offsetLeft || 0) + visual.width);
    if (Math.max(0, right - left) < requiredVisiblePixels(bounds.width, visual.width) ||
        Math.max(0, bottom - top) < requiredVisiblePixels(bounds.height, visual.height)) return false;
  }
  return bounds.width > 0 && bounds.height > 0 && visible.width > 0 && visible.height > 0 &&
    visible.width >= requiredVisiblePixels(bounds.width, viewportWidth) &&
    visible.height >= requiredVisiblePixels(bounds.height, viewportHeight);
}

function requiredVisiblePixels(elementSize, viewportSize) {
  return Math.max(1, Math.min(elementSize || 0, PROMPT_RECEIPT_MIN_VISIBLE_PX, viewportSize * PROMPT_RECEIPT_VIEWPORT_FRACTION));
}

function viewportBounds() {
  const visual = window.visualViewport;
  if (visual?.width > 0 && visual?.height > 0) {
    return { top: visual.offsetTop || 0, left: visual.offsetLeft || 0, width: visual.width, height: visual.height };
  }
  return {
    top: 0, left: 0,
    height: window.innerHeight || document.documentElement?.clientHeight || 0,
    width: window.innerWidth || document.documentElement?.clientWidth || 0,
  };
}

export function isMeaningfullyInViewport(element) {
  if (!element || typeof window === "undefined") return false;
  const rect = element.getBoundingClientRect?.();
  if (!rect) return false;
  const viewport = viewportBounds();
  const viewportHeight = viewport.height;
  const viewportWidth = viewport.width;
  let visibleTop = Math.max(rect.top, viewport.top);
  let visibleBottom = Math.min(rect.bottom, viewport.top + viewportHeight);
  let visibleLeft = Math.max(rect.left, viewport.left);
  let visibleRight = Math.min(rect.right, viewport.left + viewportWidth);

  // IntersectionObserver accounts for every overflow clip. Its geometric
  // safety net must use the same scrollport rather than treating a card below
  // `.stream-scroll` / `.mconv-stream` as visible merely because it is inside
  // the browser window. Walk all clipping ancestors too: a pane can add one
  // around the stream, and each intersection is just four cheap comparisons.
  for (const ancestor of clippingAncestors(element)) {
    const clip = ancestor.getBoundingClientRect?.();
    if (!clip) continue;
    visibleTop = Math.max(visibleTop, clip.top);
    visibleBottom = Math.min(visibleBottom, clip.bottom);
    visibleLeft = Math.max(visibleLeft, clip.left);
    visibleRight = Math.min(visibleRight, clip.right);
  }
  const visibleHeight = Math.max(0, visibleBottom - visibleTop);
  const visibleWidth = Math.max(0, visibleRight - visibleLeft);
  return rect.width > 0 && rect.height > 0 && visibleWidth > 0 && visibleHeight > 0 &&
    visibleHeight >= requiredVisiblePixels(rect.height, viewportHeight) &&
    visibleWidth >= requiredVisiblePixels(rect.width, viewportWidth);
}

function clippingAncestors(element) {
  const ancestors = new Set();
  const scrollport = element.closest?.(".stream-scroll, .mconv-stream");
  if (scrollport) ancestors.add(scrollport);
  for (let ancestor = element.parentElement; ancestor; ancestor = ancestor.parentElement) {
    const style = window.getComputedStyle?.(ancestor);
    const overflow = `${style?.overflow || ""} ${style?.overflowX || ""} ${style?.overflowY || ""}`;
    if (/(auto|scroll|hidden|clip)/.test(overflow)) ancestors.add(ancestor);
  }
  return ancestors;
}

// A pending prompt remains mounted while an overlay is open. Subscribe to only
// the visibility-relevant state so its post-commit receipt retries on close.
export function useConversationVisibilityKey() {
  const [key, setKey] = useState(() => conversationVisibilityKey(store.get()));
  useEffect(() => store.subscribe((next) => {
    setKey(conversationVisibilityKey(next));
  }), []);
  return key;
}

// conversationVisibilityKey covers in-app overlays; this additional tick is
// driven by the browser foreground event so a receipt observed while hidden
// gets another acknowledgement attempt when the app returns.
function useReceiptVisibilityKey() {
  const conversationKey = useConversationVisibilityKey();
  const [foregroundTick, setForegroundTick] = useState(0);
  useEffect(() => {
    if (typeof document === "undefined") return undefined;
    const onVisibilityChange = () => setForegroundTick(tick => tick + 1);
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => document.removeEventListener("visibilitychange", onVisibilityChange);
  }, []);
  return `${conversationKey}:${foregroundTick}`;
}

// A committed card below a scrollport is not proof it was shown. The observer
// and fallback both require the same meaningful viewport threshold instead.
export function usePendingPromptAttentionReceipt(
  ref, sessionId, pending,
  acknowledge = acknowledgeRenderedPendingAttention,
  refreshInstances = refreshAttentionInstances,
) {
  const occurrence = occurrenceKey(pending);
  const key = occurrence ? `${sessionId}:${occurrence}` : "";
  const [seenKey, setSeenKey] = useState("");
  const [acknowledgedKey, setAcknowledgedKey] = useState("");
  const [retryTick, setRetryTick] = useState(0);
  const visibilityKey = useReceiptVisibilityKey();
  const observerRef = useRef(null);
  const retryTimerRef = useRef(null);
  const retryDelayRef = useRef(RECEIPT_RETRY_INITIAL_MS);

  // A remote client can resolve a prompt between our failed acknowledgement
  // and its retry. The prompt component then unmounts, but its foreground
  // proof is retained in the session receipt store and the replacement notice
  // can finish the same acknowledgement.
  useEffect(() => {
    const remembered = rememberedPendingAttention(sessionId);
    if (key && occurrenceKey(remembered) === occurrence) setSeenKey(key);
  }, [sessionId, key, occurrence]);

  const observeViewport = () => {
    if (typeof document !== "undefined" && document.hidden) return;
    if (isMeaningfullyInViewport(ref.current)) setSeenKey(key);
  };

  const scheduleRetry = () => {
    if (retryTimerRef.current || !key || acknowledgedKey === key) return;
    if (typeof document !== "undefined" && document.hidden) return;
    const delay = retryDelayRef.current;
    retryDelayRef.current = Math.min(delay * 2, RECEIPT_RETRY_MAX_MS);
    retryTimerRef.current = setTimeout(() => {
      retryTimerRef.current = null;
      if (typeof document !== "undefined" && document.hidden) return;
      setRetryTick(tick => tick + 1);
    }, delay);
  };

  useEffect(() => () => {
    if (retryTimerRef.current) clearTimeout(retryTimerRef.current);
  }, []);

  useEffect(() => {
    if (!key || acknowledgedKey === key || !ref.current || typeof IntersectionObserver === "undefined") return undefined;
    const observer = new IntersectionObserver((entries) => {
      // Browser engines are allowed to deliver a queued observer callback
      // after the tab has been backgrounded. That is not a user-visible
      // observation; foreground geometry will establish a fresh receipt.
      if (typeof document !== "undefined" && document.hidden) return;
      if (!entries.some(isMeaningfullyIntersecting)) return;
      setSeenKey(key);
    }, { threshold: 0 });
    observerRef.current = { key, observer };
    observer.observe(ref.current);
    return () => {
      observer.disconnect();
      if (observerRef.current?.observer === observer) observerRef.current = null;
    };
  }, [key, acknowledgedKey]);

  // This is deliberately active even when IntersectionObserver exists. Some
  // iOS PWA scroll containers expose the API but never deliver its callback;
  // geometry, scroll capture, foreground and a small periodic safety tick then
  // remain a complete path to a receipt.
  useEffect(() => {
    if (!key || acknowledgedKey === key || !ref.current || typeof window === "undefined") return undefined;
    if (typeof document !== "undefined" && document.hidden) return undefined;
    observeViewport();
    window.addEventListener("scroll", observeViewport, true);
    window.addEventListener("resize", observeViewport);
    if (typeof document !== "undefined") document.addEventListener("visibilitychange", observeViewport);
    const safetyTick = setInterval(observeViewport, RECEIPT_RETRY_INITIAL_MS);
    return () => {
      window.removeEventListener("scroll", observeViewport, true);
      window.removeEventListener("resize", observeViewport);
      if (typeof document !== "undefined") document.removeEventListener("visibilitychange", observeViewport);
      clearInterval(safetyTick);
    };
  }, [key, acknowledgedKey, visibilityKey]);

  useEffect(() => {
    if (!key || seenKey !== key) return;
    // Visibility is established above by foreground geometry/observer. Durable
    // storage extends that proof across reloads, but quota/privacy failures
    // must not strand a card that is visibly mounted right now.
    rememberPendingAttention(sessionId, pending);
    let cancelled = false;
    acknowledge(sessionId, pending)
      .then((confirmed) => {
        if (cancelled) return;
        if (confirmed) {
          retryDelayRef.current = RECEIPT_RETRY_INITIAL_MS;
          // A confirmed receipt is no longer proof for a live request. Remove
          // it immediately so acknowledged rows cannot consume the bounded
          // pending-receipt capacity until their TTL expires.
          forgetPendingAttention(sessionId);
          const activeObserver = observerRef.current;
          if (activeObserver?.key === key) activeObserver.observer.disconnect();
          setAcknowledgedKey(key);
          return;
        }
        scheduleRetry();
      })
      .catch((error) => {
        if (cancelled) return;
        if (error instanceof StaleServerInstanceError) {
          // The old pending object is not safe to fence again. Refreshing the
          // roster replaces its socket; the next bounded retry uses only the
          // fresh prompt/instance delivered by that init.
          refreshInstances().finally(() => {
            if (!cancelled) scheduleRetry();
          });
          return;
        }
        scheduleRetry();
      });
    return () => { cancelled = true; };
  }, [sessionId, key, seenKey, visibilityKey, retryTick, acknowledge, refreshInstances]);

  useEffect(() => {
    if (typeof window === "undefined") return undefined;
    const retryNow = () => {
      retryDelayRef.current = RECEIPT_RETRY_INITIAL_MS;
      if (retryTimerRef.current) {
        clearTimeout(retryTimerRef.current);
        retryTimerRef.current = null;
      }
      setRetryTick(tick => tick + 1);
    };
    window.addEventListener("online", retryNow);
    return () => window.removeEventListener("online", retryNow);
  }, []);
}

// A remote resolution can remove a request while its parent is replaced by a
// detail view. The original prompt is gone, so this explicit, attributable
// receipt is rendered in the transcript tail. It is acknowledged only after
// the user has meaningfully seen that resolution notice.
export function ResolvedAttentionReceipt({
  session,
  acknowledge = acknowledgeRenderedPendingAttention,
  refreshInstances = refreshAttentionInstances,
}) {
  const pending = session.resolvedPendingAttention;
  const ownRef = useRef(null);
  const acknowledgeResolved = useCallback((sessionId, occurrence) => acknowledge(sessionId, occurrence).then((confirmed) => {
    if (confirmed) {
      forgetPendingAttention(sessionId);
      updateSession(sessionId, { resolvedPendingAttention: null });
    }
    return confirmed;
  }), [acknowledge]);
  usePendingPromptAttentionReceipt(ownRef, session.id, pending, acknowledgeResolved, refreshInstances);

  if (!pending) return null;
  return (
    <div class="attention-resolved-receipt" ref={ownRef} role="status">
      This request was resolved in another client.
    </div>
  );
}
