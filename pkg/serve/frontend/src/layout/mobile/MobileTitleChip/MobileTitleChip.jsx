import { Inbox } from "lucide-preact";
import { mobileTitleChipLabel } from "../MobileConversationScreen/attention-model.js";
import "./MobileTitleChip.css";

// MobileTitleChip — the session's name, the middle capsule of the phone's
// header (MobileChrome). Markup and CSS are the catalogue's (catalog/zones-lab.jsx
// `Phone` the `.zl-cap.zl-chip` button, zones-lab.css `.zl-chip` / `.zl-chip-name`
// / `.zl-chev`), MOVED here rather than imitated. The catalogue imports
// MobileChrome now, so this is the only title chip in the product.
//
// What is NOT the catalogue's is everything the prototype never had, grafted
// on top: it is a door to THIS session's panel (the dossier), not to the
// session list; the accessible name says so; and the inbox count stays here
// because events waiting have to be legible WITHOUT opening anything, and a
// 44px icon button has no room for a number next to its glyph.

export function MobileTitleChip({ title, open = false, onToggle, inboxCount = 0 }) {
  return (
    <button
      type="button"
      class={`zl-cap zl-chip${open ? " is-open" : ""}`}
      onClick={() => onToggle?.(!open)}
      aria-haspopup="dialog"
      aria-expanded={open}
      aria-label={mobileTitleChipLabel(title, inboxCount)}
    >
      <span class="zl-chip-name">{title}</span>
      {inboxCount > 0 && (
        <span class="zl-chip-inbox" aria-hidden="true">
          <Inbox size={12} />
          {inboxCount > 9 ? "9+" : inboxCount}
        </span>
      )}
      <svg class="zl-chev" viewBox="0 0 16 16" aria-hidden="true">
        <path d="M4.5 6.5L8 10l3.5-3.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
    </button>
  );
}
