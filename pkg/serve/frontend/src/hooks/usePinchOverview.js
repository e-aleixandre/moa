import { useRef, useEffect, useCallback } from 'preact/hooks';

/**
 * Detects two-finger pinch gestures.
 * onPinch(direction) is called with 'in' or 'out'.
 * Returns a ref to attach to the target element.
 */
export function usePinchOverview(onPinch) {
  const elRef = useRef(null);
  const gestureState = useRef({ startDist: 0, active: false });

  // Stable callback ref
  const callbackRef = useRef(onPinch);
  callbackRef.current = onPinch;

  useEffect(() => {
    const el = elRef.current;
    if (!el) return;

    function dist(touches) {
      const dx = touches[0].clientX - touches[1].clientX;
      const dy = touches[0].clientY - touches[1].clientY;
      return Math.hypot(dx, dy);
    }

    function onTouchStart(e) {
      if (e.touches.length === 2) {
        gestureState.current.startDist = dist(e.touches);
        gestureState.current.active = true;
      }
    }

    function onTouchMove(e) {
      if (!gestureState.current.active || e.touches.length !== 2) return;
      const d = dist(e.touches);
      const start = gestureState.current.startDist;
      // Pinch-in (zoom out): fingers move closer — 40% reduction
      if (d < start * 0.6) {
        gestureState.current.active = false;
        callbackRef.current('in');
      }
      // Pinch-out (zoom in): fingers spread apart — 60% increase
      if (d > start * 1.6) {
        gestureState.current.active = false;
        callbackRef.current('out');
      }
    }

    function onTouchEnd() {
      gestureState.current.active = false;
    }

    el.addEventListener('touchstart', onTouchStart, { passive: true });
    el.addEventListener('touchmove', onTouchMove, { passive: true });
    el.addEventListener('touchend', onTouchEnd, { passive: true });
    el.addEventListener('touchcancel', onTouchEnd, { passive: true });

    return () => {
      el.removeEventListener('touchstart', onTouchStart);
      el.removeEventListener('touchmove', onTouchMove);
      el.removeEventListener('touchend', onTouchEnd);
      el.removeEventListener('touchcancel', onTouchEnd);
    };
  }, []);

  return elRef;
}
