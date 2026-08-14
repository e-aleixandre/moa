import { useRef, useState, useCallback, useEffect } from "preact/hooks";

// useEdgeSwipeBack — the iOS "back" gesture for a full-screen push: a touch that
// starts within EDGE_ZONE of the left edge and travels right drags the screen
// off to reveal what it was pushed over, and releasing past a threshold (or
// flicking) commits the back navigation.
//
// It is the same shape as useSheetDismiss, one axis over: the gesture surface
// is the screen root, the drag is written IMPERATIVELY to the DOM so a touchmove
// never re-renders the transcript, and a release settles either back to rest or
// out to the edge. The chevron in the header stays the accessible path — this is
// a progressive enhancement on top of it, which is why an ambiguous or
// vertical-dominant gesture is abandoned rather than guessed at.
//
// Only touches starting at the edge qualify: anywhere else the horizontal axis
// belongs to the content (code blocks, tables, the sibling rail).

const EDGE_ZONE = 24; // px from the left edge where a back gesture may start
const BEGIN_THRESHOLD = 12; // px of rightward travel before the drag begins
const VERTICAL_SLOP = 12; // px of vertical travel that abandons the gesture
const BACK_FRACTION = 0.35; // fraction of the width past which release goes back
const FLICK_VELOCITY = 0.4; // px/ms rightward flick that goes back regardless

const SETTLE_MS = 220;
const SETTLE_EASE = "cubic-bezier(0.2, 0.7, 0.2, 1)";

function prefersReducedMotion() {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

export function useEdgeSwipeBack({ onBack }) {
  const screenRef = useRef(null);
  const [dragging, setDragging] = useState(false);

  const startRef = useRef(null); // { x, y } of a touch that began at the edge
  const activeRef = useRef(false); // has the drag actually begun
  const samplesRef = useRef([]); // recent { t, x } for release velocity
  const offsetRef = useRef(0); // last px of rightward travel
  const settleTimerRef = useRef(null);

  const paint = useCallback((dx) => {
    const el = screenRef.current;
    if (!el) return;
    el.style.transition = "none";
    el.style.transform = `translateX(${dx}px)`;
  }, []);

  // Animate to rest (0) or off the right edge, then hand control back: going
  // back unmounts this screen, springing back leaves it in place.
  const settle = useCallback(
    (toBack) => {
      const el = screenRef.current;
      const reduce = prefersReducedMotion();
      const width = el ? el.offsetWidth : window.innerWidth;
      if (el) {
        el.style.transition = reduce ? "none" : `transform ${SETTLE_MS}ms ${SETTLE_EASE}`;
        el.style.transform = toBack ? `translateX(${width}px)` : "translateX(0)";
      }
      const finish = () => {
        settleTimerRef.current = null;
        setDragging(false);
        if (toBack) onBack?.();
        else if (el) {
          el.style.transition = "";
          el.style.transform = "";
        }
      };
      if (reduce) finish();
      else settleTimerRef.current = setTimeout(finish, SETTLE_MS);
    },
    [onBack]
  );

  const onTouchStart = useCallback((e) => {
    if (e.touches.length !== 1) return;
    const t = e.touches[0];
    // Measure the edge against the screen itself, not the viewport: the screen
    // is what the gesture drags.
    const el = screenRef.current;
    const left = el ? el.getBoundingClientRect().left : 0;
    if (t.clientX - left > EDGE_ZONE) return;
    startRef.current = { x: t.clientX, y: t.clientY };
    activeRef.current = false;
    samplesRef.current = [{ t: performance.now(), x: t.clientX }];
  }, []);

  const onTouchMove = useCallback(
    (e) => {
      if (!startRef.current) return;
      const t = e.touches[0];
      const dx = t.clientX - startRef.current.x;
      const dy = t.clientY - startRef.current.y;

      if (!activeRef.current) {
        if (Math.abs(dy) > VERTICAL_SLOP || dx < 0) {
          // Vertical (let the transcript scroll) or leftward — not ours.
          startRef.current = null;
          return;
        }
        if (dx > BEGIN_THRESHOLD && dx > Math.abs(dy)) {
          activeRef.current = true;
          setDragging(true);
        } else {
          return;
        }
      }

      if (e.cancelable) e.preventDefault();
      const travel = Math.max(0, dx);
      offsetRef.current = travel;
      const now = performance.now();
      const s = samplesRef.current;
      s.push({ t: now, x: t.clientX });
      if (s.length > 6) s.shift();
      paint(travel);
    },
    [paint]
  );

  const endGesture = useCallback(() => {
    if (!activeRef.current) {
      startRef.current = null;
      return;
    }
    activeRef.current = false;
    startRef.current = null;

    const s = samplesRef.current;
    let velocity = 0;
    if (s.length >= 2) {
      const a = s[s.length - 2];
      const b = s[s.length - 1];
      const dt = b.t - a.t;
      if (dt > 0) velocity = (b.x - a.x) / dt;
    }
    const el = screenRef.current;
    const width = el ? el.offsetWidth : window.innerWidth;
    const goBack = offsetRef.current > width * BACK_FRACTION || velocity > FLICK_VELOCITY;
    settle(goBack);
  }, [settle]);

  // A cancelled touch is the system taking the gesture away (an incoming call,
  // a browser gesture, a focus change) — not the user completing it. It must
  // spring back, never navigate, so it deliberately skips the threshold and
  // velocity verdict that endGesture applies.
  const cancelGesture = useCallback(() => {
    if (!activeRef.current) {
      startRef.current = null;
      return;
    }
    activeRef.current = false;
    startRef.current = null;
    settle(false);
  }, [settle]);

  useEffect(() => () => clearTimeout(settleTimerRef.current), []);

  return {
    screenRef,
    dragging,
    swipeBind: {
      onTouchStart,
      onTouchMove,
      onTouchEnd: endGesture,
      onTouchCancel: cancelGesture,
    },
  };
}
