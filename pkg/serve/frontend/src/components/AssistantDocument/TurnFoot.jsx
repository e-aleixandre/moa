import { useState } from "preact/hooks";
import { Copy as CopyIcon, Check as CheckIcon } from "lucide-preact";
import { clockHHMM } from "../../data/util/clock.js";
import { copyToClipboard } from "../../data/util/format.js";
import "./TurnFoot.css";

// TurnFoot — one line at the end of an assistant turn, carrying the hour and
// the copy. The approved treatment is "Racimo": both sit TOGETHER in a small
// cluster on the left, so the line is a mark the width of its own content and
// not a bar the width of the measure.
//
// Why that shape and not a rail beside the turn: a rail reserves 44px on the
// right of EVERY turn, which charges the prose the width the owner had just
// asked to get back. The foot costs height instead of width, and it costs it
// once per turn.
//
// Why the cluster and not the balance (hour left, actions right): this repeats
// hundreds of times down a transcript. A cluster is one small identical mark
// at the same vertical on every turn, so scrolling past it is cheap; a balance
// reads as an empty bar, and at 390px its two ends are a screen apart. The
// cluster also behaves identically at 680 and at 390, because its width comes
// from its content rather than from the column.
//
// What it does NOT do:
//
//   · appear on a turn that is still running. There is no final response yet
//     and no hour to stamp on it, so there is no foot. The foot appearing IS
//     how a turn reads as finished, and it lands UNDER the last line rather
//     than inside it, so nothing moves when it arrives.
//   · copy the whole turn. See turnFinalResponse in data/stream-model.js: the
//     clipboard gets the last run of words, which is the answer.
//   · replace the `copy` in a code block's header. That one copies the code
//     alone. Two units, two scopes, and they look nothing alike.

export function TurnFoot({ time, text = "" }) {
  const [done, setDone] = useState(false);
  const hhmm = typeof time === "string" && !/^\d+$/.test(time) ? time : clockHHMM(time);
  // Nothing to stamp and nothing to copy is not a foot.
  if (!hhmm && !text) return null;

  return (
    <div class="zl-foot">
      {hhmm && <time class="zl-foot-time zl-data">{hhmm}</time>}
      {text && (
        <span class="zl-foot-acts">
          <button
            type="button"
            class={`zl-foot-act${done ? " is-done" : ""}`}
            aria-label="Copy the final response"
            title={done ? "Copied" : "Copy the final response"}
            onClick={() => copyToClipboard(text).then((ok) => {
              if (!ok) return;
              setDone(true);
              setTimeout(() => setDone(false), 1400);
            })}
          >
            {done ? <CheckIcon aria-hidden="true" /> : <CopyIcon aria-hidden="true" />}
          </button>
        </span>
      )}
    </div>
  );
}
