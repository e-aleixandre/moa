import { useRef, useCallback, useEffect } from 'preact/hooks';

/**
 * Touch drag-and-drop hook. Produces a long-press-initiated drag that
 * works on touch devices where HTML5 drag events aren't supported.
 *
 * Usage:
 *   const touchProps = useTouchDrag({
 *     data: { 'text/x-session-id': sess.id },
 *     onDragStart: () => {},          // optional
 *   });
 *   <div {...touchProps}>...</div>
 *
 * Drop targets register themselves via registerDropTarget(el, handlers).
 *
 * Ported verbatim from the old SPA (pkg/serve/frontend/src/hooks/
 * useTouchDrag.js) for the 5G pane grid.
 */

const LONG_PRESS_MS = 300;
const MOVE_THRESHOLD = 8; // px before cancelling long-press

// Shared state for the active drag
let activeDrag = null;
let ghostEl = null;

function cleanupGhost() {
  if (ghostEl) { ghostEl.remove(); ghostEl = null; }
  activeDrag = null;
}

// Touch drop targets register themselves here
const dropTargets = new Map(); // element → { onDragOver, onDrop, onDragLeave }

export function registerDropTarget(el, handlers) {
  dropTargets.set(el, handlers);
  return () => dropTargets.delete(el);
}

function findDropTarget(x, y) {
  for (const [el, handlers] of dropTargets) {
    const r = el.getBoundingClientRect();
    if (x >= r.left && x <= r.right && y >= r.top && y <= r.bottom) {
      return { el, handlers };
    }
  }
  return null;
}

export function useTouchDrag({ data, onDragStart, ghostClass }) {
  const timerRef = useRef(null);
  const startPosRef = useRef(null);
  const isDragging = useRef(false);
  const currentTargetRef = useRef(null);

  const handleTouchStart = useCallback((e) => {
    if (e.touches.length !== 1) return;
    const touch = e.touches[0];
    // Capture the source element synchronously: inside the setTimeout below
    // (fired ~300ms later, outside the event dispatch) e.currentTarget is null
    // in Preact, so reading it there would throw on el.cloneNode. Snapshot it now.
    const sourceEl = e.currentTarget;
    startPosRef.current = { x: touch.clientX, y: touch.clientY };
    isDragging.current = false;

    timerRef.current = setTimeout(() => {
      // The pane may have unmounted during the long-press hold.
      if (!sourceEl || !sourceEl.isConnected) return;
      isDragging.current = true;
      activeDrag = { data };
      onDragStart?.();

      // Create ghost element
      ghostEl = sourceEl.cloneNode(true);
      ghostEl.className = `touch-drag-ghost ${ghostClass || ''}`;
      const rect = sourceEl.getBoundingClientRect();
      ghostEl.style.cssText = `
        position: fixed; z-index: 9999; pointer-events: none;
        width: ${rect.width}px; opacity: 0.8;
        left: ${touch.clientX - 20}px; top: ${touch.clientY - 20}px;
        transform: scale(0.85); border-radius: 8px; overflow: hidden;
        box-shadow: 0 8px 32px rgba(0,0,0,0.4);
        transition: transform 0.1s;
      `;
      document.body.appendChild(ghostEl);

      // Haptic feedback if available
      if (navigator.vibrate) navigator.vibrate(30);
    }, LONG_PRESS_MS);
  }, [data, onDragStart, ghostClass]);

  const handleTouchMove = useCallback((e) => {
    if (!startPosRef.current) return;
    const touch = e.touches[0];

    // Cancel long-press if finger moved too far before it triggered
    if (!isDragging.current) {
      const dx = touch.clientX - startPosRef.current.x;
      const dy = touch.clientY - startPosRef.current.y;
      if (Math.hypot(dx, dy) > MOVE_THRESHOLD) {
        clearTimeout(timerRef.current);
        startPosRef.current = null;
      }
      return;
    }

    e.preventDefault();

    // Move ghost
    if (ghostEl) {
      ghostEl.style.left = (touch.clientX - 20) + 'px';
      ghostEl.style.top = (touch.clientY - 20) + 'px';
    }

    // Hit-test drop targets
    const hit = findDropTarget(touch.clientX, touch.clientY);
    const prevTarget = currentTargetRef.current;

    if (hit?.el !== prevTarget?.el) {
      if (prevTarget) prevTarget.handlers.onDragLeave?.();
      if (hit) hit.handlers.onDragOver?.(activeDrag.data);
      currentTargetRef.current = hit || null;
    }
  }, []);

  // finishDrag — idempotent teardown of any in-flight drag/long-press for this
  // hook instance. Called on touchend, touchcancel (OS/browser interruption),
  // and on unmount, so a dangling ghost / stuck global activeDrag can't survive.
  const finishDrag = useCallback((commit) => {
    clearTimeout(timerRef.current);
    if (commit && isDragging.current && currentTargetRef.current) {
      currentTargetRef.current.handlers.onDrop?.(activeDrag?.data);
      currentTargetRef.current.handlers.onDragLeave?.();
    } else if (currentTargetRef.current) {
      currentTargetRef.current.handlers.onDragLeave?.();
    }
    currentTargetRef.current = null;
    isDragging.current = false;
    startPosRef.current = null;
    cleanupGhost();
  }, []);

  const handleTouchEnd = useCallback((e) => { finishDrag(true); }, [finishDrag]);
  const handleTouchCancel = useCallback((e) => { finishDrag(false); }, [finishDrag]);

  // Tear down a dangling drag if the element unmounts mid-gesture.
  useEffect(() => () => finishDrag(false), [finishDrag]);

  return {
    onTouchStart: handleTouchStart,
    onTouchMove: handleTouchMove,
    onTouchEnd: handleTouchEnd,
    onTouchCancel: handleTouchCancel,
  };
}
