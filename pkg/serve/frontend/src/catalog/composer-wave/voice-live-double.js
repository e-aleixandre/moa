import { useContext, useEffect, useState } from "preact/hooks";
import { useVoiceLive as useRealVoiceLive } from "../../hooks/useVoiceLive.js";
import { LabVoice } from "./voice-context.js";

const noop = () => {};

// Lab double (see catalog-serve.mjs): the Composer's voice call, pinned to a
// healthy live call when ?view=composer-wave asks for it, with a running clock
// so the row reads as it does in the product.
export function useVoiceLive(sessionId, opts) {
  const real = useRealVoiceLive(sessionId, opts);
  const lab = useContext(LabVoice);
  const onCall = !!lab?.call;
  const [elapsed, setElapsed] = useState(84);
  useEffect(() => {
    if (!onCall) return undefined;
    const t = setInterval(() => setElapsed((s) => s + 1), 1000);
    return () => clearInterval(t);
  }, [onCall]);
  if (!lab) return real;
  if (!onCall) return { ...real, supported: true };
  return {
    ...real,
    phase: "live",
    active: true,
    micState: "live",
    questionsUsed: 1,
    maxQuestions: 5,
    pendingAsks: 0,
    elapsed,
    cost: { voiceUSD: 0.18 + elapsed * 0.002 },
    supported: true,
    start: noop,
    hangup: noop,
  };
}
