import { useEffect, useState } from "preact/hooks";
import { api } from "../../data/api.js";
import { addToast } from "../../data/notifications.js";
import { Segmented } from "../Segmented/Segmented.jsx";
import {
  TOKENS_PER_UNIT,
  clampToFloor,
  floorUnits,
  formatTokens,
  modeForCompactAt,
  parseUnits,
} from "./compact-at-model.js";

const MODE_OPTIONS = [
  { value: "auto", label: "Automatic" },
  { value: "custom", label: "Custom" },
];

// CompactAt — the device-wide auto-compaction threshold.
//
// It is the DEFAULT, not an override: a session that set its own limit (the
// slider in its status line) keeps it, and subagents inherit their parent's
// value before falling back to this one. So the copy says "sessions that have
// no limit of their own" rather than "all sessions", which would be a lie the
// first time someone drags a session slider.
//
// A number field, not the session's percentage slider: percent needs a window
// to be a percentage OF, and a global default applies across models with
// windows from 200k to 1M. Tokens are the only unit that means the same thing
// on all of them.
export function CompactAt() {
  const [tokens, setTokens] = useState(0);
  const [min, setMin] = useState(0);
  const [mode, setMode] = useState("auto");
  const [draft, setDraft] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [raised, setRaised] = useState(false);

  useEffect(() => {
    let live = true;
    api("GET", "/api/compact-at")
      .catch(() => null)
      .then((policy) => {
        if (!live) return;
        const at = policy?.compact_at || 0;
        setTokens(at);
        setMin(policy?.compact_at_min || 0);
        setMode(modeForCompactAt(at));
        setDraft(at ? String(Math.round(at / TOKENS_PER_UNIT)) : "");
        setLoaded(true);
      });
    return () => { live = false; };
  }, []);

  const save = (next) => {
    const { tokens: applied, clamped } = clampToFloor(next, min);
    setRaised(clamped);
    return api("PATCH", "/api/compact-at", { compact_at: applied })
      .then((policy) => {
        // The server floors it too, so what lands here is the threshold that
        // will really be used; adopt that rather than what was requested.
        const at = policy?.compact_at || 0;
        setTokens(at);
        setMode(modeForCompactAt(at));
        setDraft(at ? String(Math.round(at / TOKENS_PER_UNIT)) : "");
      })
      .catch((error) => {
        addToast({
          title: "Could not set the compaction limit",
          detail: String(error.message || error),
          type: "error",
        });
        setMode(modeForCompactAt(tokens));
      });
  };

  const onMode = (next) => {
    if (next === mode) return;
    setMode(next);
    setRaised(false);
    if (next === "auto") {
      save(0);
      return;
    }
    // Switching to Custom does not save yet: the field starts empty, and
    // committing a number is the deliberate act. Saving on the toggle alone
    // would pick a threshold nobody typed.
  };

  // Committed on blur/Enter, not on every keystroke: each save is a config
  // write, and a half-typed "1" would briefly mean 1k tokens.
  const commit = () => {
    const units = parseUnits(draft);
    if (units === null) {
      setDraft(tokens ? String(Math.round(tokens / TOKENS_PER_UNIT)) : "");
      setRaised(false);
      return;
    }
    const next = units * TOKENS_PER_UNIT;
    if (next === tokens) return;
    save(next);
  };

  const floor = floorUnits(min);

  return (
    <div class="compact-at">
      <p class="compact-at-hint" id="compact-at-hint">
        When to summarize and keep going, for sessions with no limit of their own.
        Subagents inherit their parent's.
      </p>
      <Segmented
        options={MODE_OPTIONS}
        value={mode}
        onChange={onMode}
        disabled={!loaded}
        aria-label="Auto-compaction"
        aria-describedby="compact-at-hint"
      />
      {mode === "custom" && (
        <div class="compact-at-field">
          <label class="compact-at-input">
            <input
              type="number"
              inputmode="numeric"
              min={floor}
              step={10}
              value={draft}
              placeholder={String(floor)}
              aria-label="Compact at, in thousands of tokens"
              onInput={(event) => setDraft(event.currentTarget.value)}
              onBlur={commit}
              onKeyDown={(event) => event.key === "Enter" && event.currentTarget.blur()}
            />
            <span class="compact-at-unit">k tokens</span>
          </label>
          <span class="compact-at-note">
            {raised
              ? `Minimum is ${floor}k — anything lower would compact every turn, so it was raised to ${formatTokens(tokens)}.`
              : tokens
                ? `Summarizes once a session passes ${formatTokens(tokens)}.`
                : `At least ${floor}k. Lower thresholds would compact every turn.`}
          </span>
        </div>
      )}
    </div>
  );
}
