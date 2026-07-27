import { useState, useEffect } from "preact/hooks";

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
