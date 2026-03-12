import { useRef, useCallback } from 'preact/hooks';

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
 * Drop targets use useTouchDropTarget().
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
    startPosRef.current = { x: touch.clientX, y: touch.clientY };
    isDragging.current = false;

    timerRef.current = setTimeout(() => {
      isDragging.current = true;
      activeDrag = { data };
      onDragStart?.();

      // Create ghost element
      const el = e.currentTarget;
      ghostEl = el.cloneNode(true);
      ghostEl.className = `touch-drag-ghost ${ghostClass || ''}`;
      const rect = el.getBoundingClientRect();
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

  const handleTouchEnd = useCallback((e) => {
    clearTimeout(timerRef.current);

    if (isDragging.current && currentTargetRef.current) {
      currentTargetRef.current.handlers.onDrop?.(activeDrag?.data);
      currentTargetRef.current.handlers.onDragLeave?.();
    }

    currentTargetRef.current = null;
    isDragging.current = false;
    startPosRef.current = null;
    cleanupGhost();
  }, []);

  return {
    onTouchStart: handleTouchStart,
    onTouchMove: handleTouchMove,
    onTouchEnd: handleTouchEnd,
  };
}
