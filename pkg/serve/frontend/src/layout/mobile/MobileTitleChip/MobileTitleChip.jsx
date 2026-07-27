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
// cross-session attention duty: the peach dot means ANOTHER session is blocked
// on you (the active session's own block is the inline PermissionPrompt). The
// count is deliberately not shown here — the drawer is one tap away and lists
// exactly who needs you, so a number on a 8px dot would be noise.
export function MobileTitleChip({ title, attnCount = 0, open = false, onToggle }) {
  const hasAttn = attnCount > 0;
  const label = hasAttn
    ? `${title} — sessions; ${attnCount} other session${attnCount === 1 ? "" : "s"} need you`
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
      {hasAttn && <span class="mtchip-attn" aria-hidden="true" />}
    </button>
  );
}
