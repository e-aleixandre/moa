import { CornerDownRight, Rocket } from "lucide-preact";
import "./RunModeChip.css";

// RunModeChip — whether a subagent is blocking its parent or running behind it,
// in the one spot where that fact matters: inside the fork.
//
// The mock drew a single "↳ async" chip and it read ambiguously — is that what
// the child IS, or what tapping it would DO? Both, depending on the child, and
// a chip can't be both without saying which. So the two cases stop sharing a
// shape:
//
//   already async → a flat label, no border, a state and nothing else.
//   still blocking → a bordered button with a verb ("to background"), which is
//                    the only case where a tap changes anything.
//
// Same slot, same size, deliberately different affordance: what it says is what
// the tap does, or it isn't a tap at all.
export function RunModeChip({ async, canPromote, onPromote }) {
  if (canPromote) {
    return (
      <button
        type="button"
        class="runmode runmode--action"
        onClick={onPromote}
        title="Run in the background — unblocks the parent"
      >
        <Rocket size={12} aria-hidden="true" />
        to background
      </button>
    );
  }
  if (!async) return null;
  return (
    <span class="runmode">
      <CornerDownRight size={12} aria-hidden="true" />
      async
    </span>
  );
}
