import { useCallback, useLayoutEffect, useRef } from "preact/hooks";
import { pushLayer, sheetLayer, takeKey } from "../data/overlay-layers.js";

let sheetSeq = 0;

// useSheetLayer — puts a sheet component under the one-sheet rule of
// data/overlay-layers.js. The two sheet components (components/Sheet,
// MobileSheet) call it, so every door that opens a sheet gets the rule without
// knowing it exists.
//
//   const layer = useSheetLayer({ open, present });
//   <div ref={layer.rootRef}> … </div>      // the outermost element (scrim)
//   if (!layer.takesKey(e)) return;          // in key handlers
//
// `present` is whether the sheet is on screen at all (open, or still leaving).
// `sheet: false` keeps only the place in the key order and never hides or is
// hidden: a full-screen surface drawn through Sheet, and the centred Sheet on
// the desktop, which this rule leaves as it was.
//
// A covered sheet gets `data-covered` on its root, which its stylesheet turns
// into visibility:hidden — hidden, still mounted, state intact. It is written
// on the element directly rather than rendered, so it lands in the same
// synchronous step as the stack change: when the sheet on top closes, the one
// below is visible again before focus is handed back into it.
export function useSheetLayer({ open, present, sheet = true }) {
  const idRef = useRef(null);
  if (idRef.current === null) idRef.current = `sheet-${++sheetSeq}`;
  const rootEl = useRef(null);
  const covered = useRef(false);
  const focusBeforeCover = useRef(null);
  const layer = useRef(null);
  const pop = useRef(null);

  const paint = () => rootEl.current?.toggleAttribute("data-covered", covered.current);

  const onCover = (next) => {
    const el = rootEl.current;
    if (next) {
      // What had focus in here when another sheet came over it — that sheet
      // takes focus next, and a hidden element cannot hold it anyway.
      const active = typeof document !== "undefined" ? document.activeElement : null;
      focusBeforeCover.current = el && active && el.contains(active) ? active : null;
    }
    covered.current = next;
    paint();
    if (!next) {
      const back = focusBeforeCover.current;
      focusBeforeCover.current = null;
      if (back?.isConnected) back.focus();
    }
  };

  // The stack entry lives while the sheet is on screen, leaving included.
  useLayoutEffect(() => {
    if (!present || !sheet) return undefined;
    const entry = sheetLayer(idRef.current, onCover);
    layer.current = entry;
    return () => {
      layer.current = null;
      covered.current = false;
      focusBeforeCover.current = null;
      paint();
      entry.release();
    };
  }, [present, sheet]);

  // Declared after the entry so the entry exists when `open` arrives with it,
  // and its cleanup runs before a caller's focus-restore cleanup declared
  // later: the sheet below is shown again before focus goes back into it.
  useLayoutEffect(() => {
    if (!open) return undefined;
    if (!sheet) {
      pop.current = pushLayer(idRef.current);
      return () => {
        pop.current?.();
        pop.current = null;
      };
    }
    layer.current?.open();
    return () => layer.current?.close();
  }, [open, sheet]);

  const rootRef = useCallback((el) => {
    rootEl.current = el;
    paint();
  }, []);

  // close — drop the claim at once, from the close path itself, so a key
  // handler below stops deferring to this sheet before the parent re-renders.
  const close = () => {
    if (pop.current) {
      pop.current();
      pop.current = null;
    }
    layer.current?.close();
  };

  return { id: idRef.current, rootRef, takesKey: (event) => takeKey(idRef.current, event), close };
}
