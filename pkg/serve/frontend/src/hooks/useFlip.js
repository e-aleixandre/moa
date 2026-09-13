import { useLayoutEffect, useRef } from "preact/hooks";
import { MOTION, prefersReducedMotion } from "./motion.js";

// useFlip — a list reorders by MOVING its rows (motion language, rule 4).
//
// Preact keys keep each row's DOM node when the order changes, so the node is
// the same; only its position is new. FLIP: remember where every keyed child
// was after the last render, look where it is after this one, and play the
// difference on transform so the row visibly travels from the old slot to the
// new one. Without this a session that just answered jumps to the top of the
// sidebar and the eye has to find it again; with it the eye is carried.
//
//   const listRef = useFlip(deps);
//   <div ref={listRef}>{rows.map((r) => <div key={r.id} data-flip={r.id}>...)}</div>
//
// Scope, so a list of hundreds costs nothing it does not need to:
//   · Only children carrying `data-flip` are measured, and only ONE
//     getBoundingClientRect per child per render (a read, no write, so no
//     layout is forced beyond the one the render already caused).
//   · Positions are kept in the list's own content coordinates (scroll
//     added back), so scrolling between two renders is not mistaken for a
//     reorder.
//   · A row that is new (no previous position) fades and rises in via
//     .is-flip-enter; a row that left is not animated -- it is gone, and
//     keeping it mounted for 140ms in a list this size is not worth the frame.
//   · Rows whose delta is under a pixel are skipped, and so are rows that
//     were off-screen before AND after: nobody sees them travel.
//   · Everything is Web Animations on transform, so it composites; nothing
//     here touches layout on the way.
//
// `deps` is whatever the order depends on (the row id list). Under reduced
// motion nothing is measured.
export function useFlip(deps) {
  const ref = useRef(null);
  const positions = useRef(new Map());

  useLayoutEffect(() => {
    const root = ref.current;
    if (!root) return;
    if (prefersReducedMotion()) { positions.current = new Map(); return; }
    const previous = positions.current;
    const next = new Map();
    const frame = root.getBoundingClientRect();
    const scrollTop = root.scrollTop || 0;
    const viewTop = scrollTop;
    const viewBottom = scrollTop + frame.height;
    for (const node of root.querySelectorAll("[data-flip]")) {
      const key = node.dataset.flip;
      const rect = node.getBoundingClientRect();
      const now = { top: rect.top - frame.top + scrollTop, left: rect.left - frame.left, height: rect.height };
      next.set(key, now);
      node.classList.remove("is-flip-enter");
      const was = previous.get(key);
      if (!was) {
        if (previous.size > 0) node.classList.add("is-flip-enter");
        continue;
      }
      const dy = was.top - now.top;
      const dx = was.left - now.left;
      if (Math.abs(dy) < 1 && Math.abs(dx) < 1) continue;
      const visibleWas = was.top + was.height > viewTop && was.top < viewBottom;
      const visibleNow = now.top + now.height > viewTop && now.top < viewBottom;
      if (!visibleWas && !visibleNow) continue;
      if (typeof node.animate !== "function") continue;
      node.animate(
        [{ transform: `translate(${dx}px, ${dy}px)` }, { transform: "translate(0, 0)" }],
        { duration: MOTION.base, easing: MOTION.ease },
      );
    }
    positions.current = next;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  return ref;
}
