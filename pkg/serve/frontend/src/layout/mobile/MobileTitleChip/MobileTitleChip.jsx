import { ChevronDown, Inbox } from "lucide-preact";
import { mobileTitleChipLabel } from "../MobileConversationScreen/attention-model.js";
import "./MobileTitleChip.css";

// MobileTitleChip — the session's name, the middle capsule of the phone's
// header (MobileChrome), and a door to the SessionDrawer.
//
// It used to be the WHOLE header: one pill, centred, floating alone over the
// transcript, carrying the name AND the session list AND the cross-session
// attention dot AND the inbox count. The name paid for that: 11px semibold in
// --subtext1 was the smallest type on a screen whose subject it named. Now the
// header is three capsules, the attention badge rides the sessions door next to
// it (that is where the other sessions are), and the chip does one job at the
// size the catalogue gives it: 16px, medium, in --text.
//
// It is still A door to the drawer — tapping the name you are reading to see
// the others is the gesture the phone has always had, and removing it would
// take a working affordance away to make room for a new one.
//
// The inbox count stays here. Events waiting have to be legible WITHOUT opening
// anything, and the count belongs beside a name rather than inside a 44px icon
// button, which has no room for a number next to its glyph.
export function MobileTitleChip({ title, open = false, onToggle, inboxCount = 0 }) {
  return (
    <button
      type="button"
      class={`mtchip${open ? " is-open" : ""}`}
      onClick={() => onToggle?.(!open)}
      aria-haspopup="dialog"
      aria-expanded={open}
      aria-label={mobileTitleChipLabel(title, inboxCount)}
    >
      <span class="mtchip-title">{title}</span>
      {inboxCount > 0 && (
        <span class="mtchip-inbox" aria-hidden="true">
          <Inbox size={12} />
          {inboxCount > 9 ? "9+" : inboxCount}
        </span>
      )}
      <span class="mtchip-chev" aria-hidden="true">
        <ChevronDown size={14} />
      </span>
    </button>
  );
}
