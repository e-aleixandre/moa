// wake-on-event — InboxButton: the door to the inbox, in the chrome of both
// layouts (the mobile header next to the title chip, the spine's header).
//
// One control, one grammar: an inbox glyph, and a count only while something
// is actually waiting. The badge is the mauve the rest of the product already
// uses for "unseen work" — not peach (the owner) and not yellow/red (states
// that need a decision now); a waiting event is neither urgent nor yours.
import { Inbox } from "lucide-preact";
import "./InboxButton.css";

export function InboxButton({ count = 0, open = false, onClick, size = 16 }) {
  const label = count > 0
    ? `Inbox, ${count} waiting`
    : "Inbox";
  return (
    <button
      type="button"
      class={`inbox-btn${open ? " is-open" : ""}`}
      aria-label={label}
      aria-pressed={open}
      title={label}
      onClick={onClick}
    >
      <Inbox size={size} aria-hidden="true" />
      {count > 0 && <span class="inbox-btn-badge" aria-hidden="true">{count > 9 ? "9+" : count}</span>}
    </button>
  );
}
