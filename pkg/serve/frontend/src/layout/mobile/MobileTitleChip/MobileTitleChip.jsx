import { ChevronDown, Inbox } from "lucide-preact";
import { useRef } from "preact/hooks";
import { mobileTitleChipLabel, mobileTitleChipPresentation, nextMobileTitleRipple } from "../MobileConversationScreen/attention-model.js";
import "./MobileTitleChip.css";

// MobileTitleChip — the floating session title, centred over the top of the
// mobile conversation, and the ONLY door to the session list.
//
// Mobile has no header: the transcript runs to the top edge of the phone and
// this chip floats over it on a blurred pill, so the conversation keeps the
// full height while the title stays legible. Tapping it opens the SessionDrawer,
// which unfurls directly beneath — the chip is the anchor, which is why the
// list reads as belonging to the title rather than arriving from nowhere.
//
// It replaces the status line's explicit "Sessions" door, and inherits its
// cross-session attention duty. Errors, human-input requests, and new results
// keep their winning red, yellow, or mauve color. The active session is inline.
//
// The inbox count rides on the chip too. The Inbox door itself now lives inside
// the drawer, but "events are waiting" has to stay visible WITHOUT opening it,
// and the chip is the only permanent chrome left at the top of the phone.
export function MobileTitleChip({ title, attention = {}, open = false, onToggle, inboxCount = 0 }) {
  const presentation = mobileTitleChipPresentation(attention);
  const arrivalRef = useRef(0);
  const rippleRef = useRef(0);
  const nextRipple = nextMobileTitleRipple(arrivalRef.current, rippleRef.current, attention);
  arrivalRef.current = nextRipple.arrival;
  rippleRef.current = nextRipple.ripple;
  const label = mobileTitleChipLabel(title, attention, inboxCount);
  return (
    <button
      type="button"
      class={`mtchip${open ? " is-open" : ""}${presentation.hasAttention ? ` has-attention mtchip-attention-${presentation.tone}` : ""}`}
      onClick={() => onToggle?.(!open)}
      aria-haspopup="dialog"
      aria-expanded={open}
      aria-label={label}
    >
      <span class="mtchip-title">{title}</span>
      {inboxCount > 0 && (
        <span class="mtchip-inbox" aria-hidden="true">
          <Inbox size={11} />
          {inboxCount > 9 ? "9+" : inboxCount}
        </span>
      )}
      <span class="mtchip-chev" aria-hidden="true">
        <ChevronDown size={12} />
      </span>
      {presentation.hasAttention && (
        <span
          key={rippleRef.current}
          class={`mtchip-attn mtchip-attn-${presentation.tone}${presentation.tone === "unseen" ? " mtchip-attn-unseen" : ""}`}
          aria-hidden="true"
        />
      )}
    </button>
  );
}
