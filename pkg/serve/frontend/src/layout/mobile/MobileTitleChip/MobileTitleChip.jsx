import { ChevronDown } from "lucide-preact";
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
// cross-session attention duty. Urgent blocks use peach; an otherwise-idle
// session with a new result uses mauve. The active session is handled inline.
export function MobileTitleChip({ title, attention = {}, open = false, onToggle }) {
  const urgent = attention.urgent || 0;
  const unseen = attention.unseen || 0;
  const count = urgent + unseen;
  const hasAttn = count > 0;
  const label = hasAttn
    ? `${title} — sessions; ${count} other session${count === 1 ? "" : "s"} need attention`
    : `${title} — sessions`;
  return (
    <button
      type="button"
      class={`mtchip${open ? " is-open" : ""}`}
      onClick={() => onToggle?.(!open)}
      aria-haspopup="dialog"
      aria-expanded={open}
      aria-label={label}
    >
      <span class="mtchip-title">{title}</span>
      <span class="mtchip-chev" aria-hidden="true">
        <ChevronDown size={12} />
      </span>
      {hasAttn && <span class={`mtchip-attn${urgent ? "" : " mtchip-attn-unseen"}`} aria-hidden="true" />}
    </button>
  );
}
