import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { tap } from "../data/haptics.js";
import { MOTION, prefersReducedMotion } from "./motion.js";
import { classifyDrag, releaseVelocity, shouldDismiss } from "../data/dismiss-gesture.js";

// The drawer is mounted only after an opening drag has crossed the intent
// threshold. From then on its transform is written directly so moving a finger
// does not re-render the conversation beneath it. Opening starts at the left
// edge; once open, a sideways drag from anywhere — the panel or the veil —
// closes it, and so does a quick leftward flick (data/dismiss-gesture.js).
const EDGE_ZONE = 24;
const OPEN_FRACTION = 0.35;
const SETTLE_MS = MOTION.base;
const SETTLE_EASE = MOTION.ease;

// useEdgeSwipeDrawer — the phone conversation's sessions drawer gesture.
// Unlike pushed work screens, this only opts in at the conversation root.
// Keeping it a sibling of useEdgeSwipeBack avoids making that established back
// gesture configurable and accidentally widening its scope.
export function useEdgeSwipeDrawer({ open, enabled, onOpen, onClose }) {
  const surfaceRef = useRef(null);
  const panelRef = useRef(null);
  const [dragging, setDragging] = useState(false);
  const openRef = useRef(open);
  const enabledRef = useRef(enabled);
  const startRef = useRef(null);
  const openingRef = useRef(false);
  const activeRef = useRef(false);
  // Which side of the commit threshold the finger was on at the last move, so
  // the tick fires on the crossing and not on every frame past it.
  const pastThresholdRef = useRef(false);
  const travelRef = useRef(0);
  const samplesRef = useRef([]); // recent { t, v } (v = clientX) for release velocity
  const offsetRef = useRef(0);
  const settleTimerRef = useRef(null);
  const settlingRef = useRef(false);

  openRef.current = open;
  enabledRef.current = enabled;

  const width = useCallback(() => panelRef.current?.offsetWidth || window.innerWidth, []);
  const paint = useCallback((offset) => {
    const panel = panelRef.current;
    if (!panel) return;
    panel.style.transition = "none";
    panel.style.transform = `translateX(${offset}px)`;
  }, []);
  const clearPaint = useCallback(() => {
    const panel = panelRef.current;
    if (!panel) return;
    panel.style.transition = "";
    panel.style.transform = "";
  }, []);

  const settle = useCallback((keepOpen) => {
    const panel = panelRef.current;
    settlingRef.current = true;
    const reduce = prefersReducedMotion();
    if (panel) {
      panel.style.transition = reduce ? "none" : `transform ${SETTLE_MS}ms ${SETTLE_EASE}`;
      panel.style.transform = keepOpen ? "translateX(0)" : `translateX(${-width()}px)`;
    }
    const finish = () => {
      settleTimerRef.current = null;
      clearPaint();
      startRef.current = null;
      activeRef.current = false;
      offsetRef.current = 0;
      travelRef.current = 0;
      setDragging(false);
      settlingRef.current = false;
    };
    if (reduce) finish();
    else settleTimerRef.current = setTimeout(finish, SETTLE_MS);
  }, [clearPaint, width]);

  const setPanel = useCallback((panel) => {
    panelRef.current = panel;
    // The first qualifying move mounted the drawer. Paint its current location
    // as soon as the ref exists, rather than flashing the CSS open endpoint.
    if (panel && activeRef.current) {
      const offset = openingRef.current
        ? -panel.offsetWidth + travelRef.current
        : -travelRef.current;
      offsetRef.current = Math.min(0, Math.max(-panel.offsetWidth, offset));
      paint(offsetRef.current);
    }
  }, [paint]);

  const onTouchStart = useCallback((e) => {
    if (!enabledRef.current || settlingRef.current || e.touches.length !== 1) return;
    const touch = e.touches[0];
    if (openRef.current) {
      if (!panelRef.current) return;
    } else {
      const left = surfaceRef.current?.getBoundingClientRect().left || 0;
      if (touch.clientX - left > EDGE_ZONE) return;
    }
    startRef.current = { x: touch.clientX, y: touch.clientY };
    openingRef.current = !openRef.current;
    activeRef.current = false;
    samplesRef.current = [{ t: performance.now(), v: touch.clientX }];
    // An opening gesture starts closed, a closing one starts past the
    // threshold: either way the first crossing is the one worth feeling.
    pastThresholdRef.current = !openingRef.current;
  }, []);

  const onTouchMove = useCallback((e) => {
    if (!startRef.current) return;
    const touch = e.touches[0];
    const dx = touch.clientX - startRef.current.x;
    const dy = touch.clientY - startRef.current.y;
    const opening = openingRef.current;

    if (!activeRef.current) {
      const verdict = classifyDrag(dx, dy, { axis: "x", sign: opening ? 1 : -1 });
      if (verdict === "abandon") {
        startRef.current = null;
        return;
      }
      if (verdict === "pending") return;
      activeRef.current = true;
      setDragging(true);
      if (opening) onOpen?.();
    }

    if (e.cancelable) e.preventDefault();
    const panelWidth = width();
    travelRef.current = dx;
    const offset = opening
      ? Math.min(0, Math.max(-panelWidth, -panelWidth + dx))
      : Math.min(0, Math.max(-panelWidth, dx));
    offsetRef.current = offset;
    paint(offset);
    const samples = samplesRef.current;
    samples.push({ t: performance.now(), v: touch.clientX });
    if (samples.length > 12) samples.shift();

    // The tick that tells the finger what releasing now would do, fired on the
    // crossing rather than the position so that hovering around the threshold
    // does not buzz. Only felt in the native container: iOS gives a web app no
    // haptics at all (data/haptics.js).
    const travelled = opening ? panelWidth + offset : -offset;
    const past = opening
      ? travelled > panelWidth * OPEN_FRACTION
      : shouldDismiss({ distance: travelled, size: panelWidth, velocity: 0 });
    if (past !== pastThresholdRef.current) {
      pastThresholdRef.current = past;
      tap("select");
    }
  }, [onOpen, paint, width]);

  const finishGesture = useCallback((cancelled = false) => {
    if (settlingRef.current || !activeRef.current) {
      startRef.current = null;
      return;
    }
    activeRef.current = false;
    const opening = openingRef.current;
    const travelled = opening ? width() + offsetRef.current : -offsetRef.current;
    const commit = !cancelled && (opening
      ? travelled > width() * OPEN_FRACTION
      : shouldDismiss({ distance: travelled, size: width(), velocity: -releaseVelocity(samplesRef.current) }));
    const keepOpen = opening ? commit : !commit;
    // Closing updates the overlay state now, while the inline transform keeps
    // the panel under this gesture until the slide has reached its edge.
    // Waiting until after the slide leaves a live dialog above the next tap.
    if (!keepOpen) onClose?.();
    settle(keepOpen);
  }, [onClose, settle, width]);

  useEffect(() => () => {
    clearTimeout(settleTimerRef.current);
    clearPaint();
  }, [clearPaint]);

  return {
    surfaceRef,
    panelRef: setPanel,
    dragging,
    swipeBind: {
      onTouchStart,
      onTouchMove,
      onTouchEnd: () => finishGesture(false),
      onTouchCancel: () => finishGesture(true),
    },
  };
}
