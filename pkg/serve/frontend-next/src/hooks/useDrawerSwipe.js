import { useRef, useState, useCallback, useEffect } from "preact/hooks";

// useDrawerSwipe — a real swipe-down gesture that opens the mobile SessionDrawer
// (MOBILE-POLISH-SPEC §4). The header (and session strip) are the gesture
// surface: a touch that moves net DOWNWARD past a small threshold starts a drag;
// horizontal-dominant moves are ignored so the strip can still scroll. During
// the drag the drawer follows the finger — the sheet's translateY and the veil's
// opacity are written IMPERATIVELY to the DOM (via the refs this hook owns) so a
// touchmove never re-renders the whole conversation screen. On release the
// gesture settles: past 40% of the sheet's travel OR a downward flick faster than
// FLICK_VELOCITY opens it, otherwise it springs back closed.
//
// A plain tap (no drag) is left untouched, so the handle's own onClick (and any
// button under the finger) still fires — the drag is a progressive enhancement
// on top of the accessible tap path.

const BEGIN_THRESHOLD = 12; // px of net downward travel before a drag begins
const HORIZONTAL_SLOP = 10; // px of horizontal travel that hands off to the strip
const OPEN_FRACTION = 0.4; // fraction of sheet travel past which release opens
const FLICK_VELOCITY = 0.5; // px/ms downward flick that opens regardless of travel

// Settle timings/curves — mirror SessionDrawer.css so the imperative settle
// matches the CSS enter/leave transitions.
const OPEN_MS = 260;
const OPEN_EASE = "cubic-bezier(0.2, 0.7, 0.2, 1)";
const CLOSE_MS = 220;
const CLOSE_EASE = "cubic-bezier(0.4, 0, 1, 1)";
const VEIL_MS = 160;

function prefersReducedMotion() {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

export function useDrawerSwipe({ onOpen }) {
  const sheetRef = useRef(null);
  const veilRef = useRef(null);
  const [dragging, setDragging] = useState(false);

  const startRef = useRef(null); // { x, y } of the touch that might become a drag
  const activeRef = useRef(false); // has a drag actually begun
  const samplesRef = useRef([]); // recent { t, y } for release velocity
  const progressRef = useRef(0); // last drag progress 0..1
  const settleTimerRef = useRef(null); // pending settle() finish timeout

  // Write the sheet/veil to a given progress (0 = closed, 1 = open) with no
  // transition — the direct-manipulation path during a drag.
  const paint = useCallback((p) => {
    const sheet = sheetRef.current;
    const veil = veilRef.current;
    if (sheet) {
      sheet.style.transition = "none";
      sheet.style.transform = `translateY(${(1 - p) * 100}%)`;
    }
    if (veil) {
      veil.style.transition = "none";
      veil.style.opacity = String(p);
    }
  }, []);

  // Animate the sheet/veil to the open (target 1) or closed (target 0) rest
  // position, then hand control back to React: opening commits onOpen(), closing
  // leaves `open` false so the drawer unmounts. Reduced motion snaps instantly.
  //
  // The inline transform/opacity are DELIBERATELY left in place here — clearing
  // them from the hook would, during the drag→open handoff, drop the sheet back
  // to its CSS closed rest state for a frame before React applies `.is-open`
  // (a visible jump). SessionDrawer owns the cleanup: it clears the inline
  // styles only once it has committed `.is-open` (open && entered && !dragging).
  const settle = useCallback(
    (toOpen) => {
      const sheet = sheetRef.current;
      const veil = veilRef.current;
      const reduce = prefersReducedMotion();
      const ms = toOpen ? OPEN_MS : CLOSE_MS;
      const ease = toOpen ? OPEN_EASE : CLOSE_EASE;
      if (sheet) {
        sheet.style.transition = reduce ? "none" : `transform ${ms}ms ${ease}`;
        sheet.style.transform = toOpen ? "translateY(0)" : "translateY(100%)";
      }
      if (veil) {
        veil.style.transition = reduce ? "none" : `opacity ${VEIL_MS}ms ease`;
        veil.style.opacity = toOpen ? "1" : "0";
      }
      const finish = () => {
        settleTimerRef.current = null;
        if (toOpen) onOpen?.();
        setDragging(false);
      };
      if (reduce) finish();
      else settleTimerRef.current = setTimeout(finish, ms);
    },
    [onOpen]
  );

  const onTouchStart = useCallback((e) => {
    if (e.touches.length !== 1) return;
    const t = e.touches[0];
    startRef.current = { x: t.clientX, y: t.clientY };
    activeRef.current = false;
    samplesRef.current = [{ t: performance.now(), y: t.clientY }];
  }, []);

  const onTouchMove = useCallback(
    (e) => {
      if (!startRef.current) return;
      const t = e.touches[0];
      const dx = t.clientX - startRef.current.x;
      const dy = t.clientY - startRef.current.y;

      if (!activeRef.current) {
        // Not yet a drag — decide whether this gesture is ours.
        if (Math.abs(dx) > HORIZONTAL_SLOP || dy < -HORIZONTAL_SLOP) {
          // Horizontal (let the strip scroll) or upward — abandon.
          startRef.current = null;
          return;
        }
        if (dy > BEGIN_THRESHOLD && dy > Math.abs(dx)) {
          activeRef.current = true;
          setDragging(true); // mounts the drawer in its dragging state
        } else {
          return;
        }
      }

      // Active drag: follow the finger and stop the page from scrolling.
      if (e.cancelable) e.preventDefault();
      const sheet = sheetRef.current;
      const travel = sheet ? sheet.offsetHeight : window.innerHeight;
      const p = Math.max(0, Math.min(1, dy / travel));
      progressRef.current = p;
      const now = performance.now();
      const s = samplesRef.current;
      s.push({ t: now, y: t.clientY });
      if (s.length > 6) s.shift();
      paint(p);
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

    // Downward velocity from the last two recent samples (px/ms).
    const s = samplesRef.current;
    let velocity = 0;
    if (s.length >= 2) {
      const a = s[s.length - 2];
      const b = s[s.length - 1];
      const dt = b.t - a.t;
      if (dt > 0) velocity = (b.y - a.y) / dt;
    }
    const toOpen = progressRef.current > OPEN_FRACTION || velocity > FLICK_VELOCITY;
    settle(toOpen);
  }, [settle]);

  // Cancel any pending settle finish if the hook unmounts mid-animation, so it
  // can't call onOpen/setDragging after the component is gone.
  useEffect(() => () => clearTimeout(settleTimerRef.current), []);

  return {
    sheetRef,
    veilRef,
    dragging,
    bind: {
      onTouchStart,
      onTouchMove,
      onTouchEnd: endGesture,
      onTouchCancel: endGesture,
    },
  };
}
