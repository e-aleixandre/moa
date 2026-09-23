import { useRef, useState, useCallback, useEffect } from "preact/hooks";
import { MOTION, prefersReducedMotion } from "./motion.js";
import { classifyDrag, releaseVelocity, sheetDragBlocked, shouldDismiss } from "../data/dismiss-gesture.js";

// useSheetDismiss — a real swipe-down gesture that DISMISSES a mobile bottom
// sheet (MobileSheet, the phone pickers). `dragBind` goes on the WHOLE sheet:
// a touch anywhere on it — grabber, head, or content — can drag it down, as
// in iOS sheets. The content keeps its own gestures: a touch on the field
// being edited, or inside a scroller that is not at its top, is left alone
// (dragging down scrolls back first), and upward or sideways moves are the
// content's too.
// During the drag the sheet follows the finger — the sheet's translateY and
// the veil's opacity are written IMPERATIVELY to the DOM (via the refs this
// hook owns) so a touchmove never re-renders the whole conversation screen.
// On release it closes past a capped fraction of its height or on a downward
// flick (data/dismiss-gesture.js), otherwise it springs back open.
//
// A plain tap (no drag) is left untouched, so the grab button's own onClick (and
// any button under the finger) still fires — the drag is a progressive
// enhancement on top of the accessible tap path.

// Settle timings/curves come from the motion language (hooks/motion.js), the
// same numbers MobileSheet.css and SessionDrawer.css transition with, so a
// released finger settles exactly like a tap would have.
const OPEN_MS = MOTION.base;
const OPEN_EASE = MOTION.ease;
const CLOSE_MS = MOTION.exitBase;
const CLOSE_EASE = MOTION.easeExit;
const VEIL_MS = MOTION.exitBase;

export function useSheetDismiss({ onClose }) {
  const sheetRef = useRef(null);
  const veilRef = useRef(null);
  const [dragging, setDragging] = useState(false);

  const startRef = useRef(null); // { x, y } of the touch that might become a drag
  const activeRef = useRef(false); // has a drag actually begun
  const samplesRef = useRef([]); // recent { t, v } (v = clientY) for release velocity
  const progressRef = useRef(1); // last drag progress 1..0 (1 = fully open)
  const settleTimerRef = useRef(null); // pending settle() finish timeout

  // Write the sheet/veil to a given progress (1 = open, 0 = closed) with no
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
  // position, then hand control back to React: closing commits onClose(),
  // springing back open leaves `open` true so the drawer stays mounted.
  // Reduced motion snaps instantly.
  //
  // The inline transform/opacity are DELIBERATELY left in place here — clearing
  // them from the hook would, during the drag→open handoff, drop the sheet back
  // to its CSS closed rest state for a frame before React re-applies `.is-open`
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
        if (!toOpen) onClose?.();
        setDragging(false);
      };
      if (reduce) finish();
      else settleTimerRef.current = setTimeout(finish, ms);
    },
    [onClose]
  );

  const onTouchStart = useCallback((e) => {
    startRef.current = null;
    if (e.touches.length !== 1) return;
    if (sheetDragBlocked(e.target, e.currentTarget, (el) => getComputedStyle(el), document.activeElement)) return;
    const t = e.touches[0];
    startRef.current = { x: t.clientX, y: t.clientY };
    activeRef.current = false;
    samplesRef.current = [{ t: performance.now(), v: t.clientY }];
  }, []);

  const onTouchMove = useCallback(
    (e) => {
      if (!startRef.current) return;
      const t = e.touches[0];
      const dx = t.clientX - startRef.current.x;
      const dy = t.clientY - startRef.current.y;

      if (!activeRef.current) {
        const verdict = classifyDrag(dx, dy, { axis: "y", sign: 1 });
        if (verdict === "abandon") {
          startRef.current = null;
          return;
        }
        if (verdict === "pending") {
          // Mainly downward so far: hold the page still so the content's
          // overscroll does not bounce under a drag that is about to start.
          if (dy > 0 && dy >= Math.abs(dx) && e.cancelable) e.preventDefault();
          return;
        }
        activeRef.current = true;
        setDragging(true); // the sheet switches to its dragging state
      }

      // Active drag: follow the finger and stop the page from scrolling.
      if (e.cancelable) e.preventDefault();
      const sheet = sheetRef.current;
      const travel = sheet ? sheet.offsetHeight : window.innerHeight;
      // Progress runs 1 → 0 as the finger drags the sheet down.
      const p = Math.max(0, Math.min(1, 1 - dy / travel));
      progressRef.current = p;
      const s = samplesRef.current;
      s.push({ t: performance.now(), v: t.clientY });
      if (s.length > 12) s.shift();
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

    const sheet = sheetRef.current;
    const size = sheet ? sheet.offsetHeight : window.innerHeight;
    const close = shouldDismiss({
      distance: (1 - progressRef.current) * size,
      size,
      velocity: releaseVelocity(samplesRef.current),
    });
    settle(!close);
  }, [settle]);

  // Cancel any pending settle finish if the hook unmounts mid-animation, so it
  // can't call onClose/setDragging after the component is gone.
  useEffect(() => () => clearTimeout(settleTimerRef.current), []);

  return {
    sheetRef,
    veilRef,
    dragging,
    dragBind: {
      onTouchStart,
      onTouchMove,
      onTouchEnd: endGesture,
      onTouchCancel: endGesture,
    },
  };
}
