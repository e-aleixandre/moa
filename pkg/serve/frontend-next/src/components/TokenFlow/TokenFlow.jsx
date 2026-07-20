import { useRef, useState, useEffect } from "preact/hooks";
import { fmtTokens } from "../../data/util/format.js";
import "./TokenFlow.css";

// TokenFlow — the live per-run token heartbeat (↑ input · ↓ output). Shared by
// the desktop StatusStrip and the mobile status line so both densities pulse
// identically (parity). It is the "the agent is alive / chewing" signal, not
// conversation accounting — the values are per-RUN and reset each run.
//
// When a value CHANGES (tokens flow in on ↑ or out on ↓) the corresponding
// arrow+number pulses a soft color for a beat: ↑ blue (input coming in), ↓ teal
// (output coming out). It's a glance-only breath of life; reduced-motion users
// get the color tint without the fade animation (CSS decides).
//
// `variant` only tweaks the trailing unit label: "strip" appends " tok" (the
// desktop line has room), "compact" (mobile) omits it.

// PULSE_MS must match the CSS animation duration (--token-pulse below).
const PULSE_MS = 900;

// usePulse returns a boolean that flips true for PULSE_MS whenever `value`
// increases (a decrease/reset — e.g. a new run zeroing the tally — never
// pulses; only real inflow/outflow lights up). The timer is self-cancelling so
// rapid successive bumps re-arm a single pulse rather than stacking.
function usePulse(value) {
  const prev = useRef(value);
  const [pulsing, setPulsing] = useState(false);
  const timer = useRef(null);
  useEffect(() => {
    const grew = typeof value === "number" && typeof prev.current === "number" && value > prev.current;
    prev.current = value;
    if (!grew) {
      // A decrease or reset (e.g. a new run zeroing the tally) must never leave
      // a pulse stuck lit: clear any in-flight pulse + its timer. Without this,
      // if the value resets before the pulse timer fires, the effect cleanup
      // cancels the timer and the .pulse class stays on forever.
      clearTimeout(timer.current);
      setPulsing(false);
      return;
    }
    setPulsing(true);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setPulsing(false), PULSE_MS);
    return () => clearTimeout(timer.current);
  }, [value]);
  return pulsing;
}

export function TokenFlow({ up, down, variant = "strip" }) {
  const upPulse = usePulse(up);
  const downPulse = usePulse(down);
  const unit = variant === "strip" ? " tok" : "";
  return (
    <span class="token-flow" aria-label={`${up || 0} input, ${down || 0} output tokens this run`}>
      <span class={`token-flow-in${upPulse ? " pulse" : ""}`}>↑ {fmtTokens(up || 0)}</span>
      <span class="token-flow-sep" aria-hidden="true"> · </span>
      <span class={`token-flow-out${downPulse ? " pulse" : ""}`}>↓ {fmtTokens(down || 0)}{unit}</span>
    </span>
  );
}
