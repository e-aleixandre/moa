import { useState, useEffect } from "preact/hooks";

// useElapsed re-renders every second while `startedAt` (ms epoch) is set,
// returning the elapsed ms since then. Shared by the tool-group live row on
// both frontends (the shared ActivityLedger card); the caller shows the timer
// only past its own threshold (3s — fast tools never flicker a timer).
export function useElapsed(startedAt) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!startedAt) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [startedAt]);
  if (!startedAt) return 0;
  return Math.max(0, now - startedAt);
}
