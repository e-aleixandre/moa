import { useState, useEffect } from "preact/hooks";

// useOffscreenLiveSurface — true when live delegation/bash surfaces exist in
// the stream but are all scrolled OUT of view, which is exactly when the Live
// Dock should mirror them (SUBAGENTS-PERSISTENT-SPEC §1.1). It watches every
// element tagged `[data-live-surface]` inside `root` with an
// IntersectionObserver and applies hysteresis so a surface hovering on the
// viewport edge doesn't flip the dock on and off.
//
// `root` is the stream's scroll container ELEMENT (state, not a ref, so a
// Stream remount re-runs this and re-binds the observer). `active` gates the
// whole thing: with nothing live we don't observe and report false (dock
// height 0, no "empty dock" state).
export function useOffscreenLiveSurface(root, active) {
  const [offscreen, setOffscreen] = useState(false);

  useEffect(() => {
    if (!active) {
      setOffscreen(false);
      return;
    }
    if (!root || typeof IntersectionObserver === "undefined") {
      // No observer available (old engine / test env): fall back to the simpler
      // "visible whenever there's life" rule the spec accepts as first-iter.
      setOffscreen(true);
      return;
    }

    // Show the dock when the MOST-visible live surface is below 0.15 visible;
    // hide it only above 0.30 — the dead band prevents edge flicker.
    const HIDE_BELOW = 0.15;
    const SHOW_ABOVE = 0.3;

    let els = [];
    // Per-element visible ratio, keyed by the element itself.
    const ratios = new Map();

    const recompute = () => {
      let max = 0;
      for (const r of ratios.values()) if (r > max) max = r;
      // No live surface in the DOM (e.g. it settled/ended before its data left
      // liveTrayAgents): mirror unconditionally while `active` holds.
      if (!els.length) {
        setOffscreen(true);
        return;
      }
      setOffscreen((prev) =>
        prev ? max < SHOW_ABOVE : max < HIDE_BELOW
      );
    };

    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) ratios.set(e.target, e.intersectionRatio);
        recompute();
      },
      { root, threshold: [0, HIDE_BELOW, SHOW_ABOVE, 0.6, 1] }
    );

    // The set of live surfaces changes as the stream re-renders; re-scan on a
    // MutationObserver so the IO always watches the current live surfaces.
    const rescan = () => {
      const next = Array.from(root.querySelectorAll("[data-live-surface]"));
      if (next.length === els.length && next.every((n, i) => n === els[i])) return;
      els.forEach((el) => {
        io.unobserve(el);
        ratios.delete(el);
      });
      els = next;
      els.forEach((el) => io.observe(el));
      recompute();
    };
    rescan();
    const mo = new MutationObserver(rescan);
    mo.observe(root, { childList: true, subtree: true });

    return () => {
      mo.disconnect();
      io.disconnect();
    };
  }, [root, active]);

  return offscreen;
}

// useSpotlight — rotates an index across `count` items every `intervalMs`, so
// the compact dock can cycle "what is each live thing doing" without expanding
// (SUBAGENTS-PERSISTENT-SPEC §1.2). Rotation stops (and pins index 0) under
// prefers-reduced-motion or when there's a single item.
export function useSpotlight(count, intervalMs = 4000) {
  const [index, setIndex] = useState(0);

  useEffect(() => {
    if (index >= count) setIndex(0);
  }, [count, index]);

  useEffect(() => {
    if (count <= 1) {
      setIndex(0);
      return;
    }
    const reduced =
      typeof matchMedia !== "undefined" &&
      matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (reduced) return;
    const t = setInterval(() => setIndex((i) => (i + 1) % count), intervalMs);
    return () => clearInterval(t);
  }, [count, intervalMs]);

  return Math.min(index, Math.max(0, count - 1));
}
