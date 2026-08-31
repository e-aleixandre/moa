import { useEffect, useState } from "preact/hooks";
import { api } from "../../data/api.js";
import { addToast } from "../../data/notifications.js";
import { Segmented } from "../Segmented/Segmented.jsx";

// The three strategies, in order of how much they intervene. Labels avoid the
// word "compaction" twice over: the section heading already says it.
const STRATEGY_OPTIONS = [
  { value: "plain", label: "None" },
  { value: "notify", label: "Warn" },
  { value: "prepare", label: "Prepare" },
];

const HINTS = {
  plain: "Summarize with no warning.",
  notify: "Warn the agent as the limit approaches, so it can save unfinished work first. Free.",
  prepare: "Give the agent a full turn to write things down before summarizing. Costs a request.",
};

// CompactStrategy — what the agent gets before an automatic compaction.
//
// An automatic compaction arrives mid-task, and whatever the agent had worked
// out but never wrote down is replaced by a summary. This is the control for
// that moment.
//
// Subagents are never warned regardless of this setting: they have neither
// memory nor the ephemeral checkpoint, so the only thing a warning could
// produce is stray files.
export function CompactStrategy() {
  const [strategy, setStrategy] = useState("notify");
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    let live = true;
    api("GET", "/api/compact-strategy")
      .catch(() => null)
      .then((policy) => {
        if (!live) return;
        setStrategy(policy?.compact_strategy || "notify");
        setLoaded(true);
      });
    return () => { live = false; };
  }, []);

  const onChange = (next) => {
    if (next === strategy) return;
    const previous = strategy;
    setStrategy(next);
    api("PATCH", "/api/compact-strategy", { compact_strategy: next })
      .then((policy) => {
        setStrategy(policy?.compact_strategy || next);
      })
      .catch((error) => {
        addToast({
          title: "Could not set the compaction strategy",
          detail: String(error.message || error),
          type: "error",
        });
        setStrategy(previous);
      });
  };

  return (
    <div class="compact-at">
      <p class="compact-at-hint" id="compact-strategy-hint">
        {HINTS[strategy]}
      </p>
      <Segmented
        options={STRATEGY_OPTIONS}
        value={strategy}
        onChange={onChange}
        disabled={!loaded}
        aria-label="Before compacting"
        aria-describedby="compact-strategy-hint"
      />
    </div>
  );
}
