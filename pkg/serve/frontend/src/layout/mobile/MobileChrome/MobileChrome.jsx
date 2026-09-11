import { Menu, Plus } from "lucide-preact";
import { useRef } from "preact/hooks";
import {
  mobileSessionsDoorLabel,
  mobileTitleChipPresentation,
  nextMobileTitleRipple,
} from "../MobileConversationScreen/attention-model.js";
import { MobileTitleChip } from "../MobileTitleChip/MobileTitleChip.jsx";
import "./MobileChrome.css";

// MobileChrome — the phone's header: three capsules floating over the top of
// the transcript. Sessions on the left, this session's name in the middle, new
// session on the right.
//
// It replaces a single centred pill. The pill had to be the door to everything
// the phone could not show, which is why it carried the session list AND the
// cross-session attention dot AND the inbox count at 11px. Three doors in one
// 30px target is what "encoge y apaga" looked like in practice: the name of the
// thing you are reading was the smallest type on the screen.
//
// Nothing here invents a destination. The left capsule opens the SessionDrawer
// that already exists, and the right capsule opens that drawer on its create
// step — the same openDrawer("new") the empty state's "New session" button has
// always used. The middle capsule opens THIS session's panel (the dossier the
// desktop opens from its crumb): it used to open the session list too, which
// left the left capsule and the name pointing at the same place and the
// session's own dossier with no door on the phone at all.
//
// The cross-session attention badge moves here, onto the sessions door: it says
// "another session wants you", so it belongs on the button that goes to them
// rather than on the name of the one you are already reading. Its colours are
// unchanged (red error, yellow waiting, mauve unread) and so is its finite
// ripple, re-keyed by arrival so a new arrival restarts it without looping.
export function MobileChrome({
  title,
  attention = {},
  open = false,
  onToggle,
  panelOpen = false,
  onPanel,
  onNew,
  inboxCount = 0,
}) {
  const presentation = mobileTitleChipPresentation(attention);
  const arrivalRef = useRef(0);
  const rippleRef = useRef(0);
  const nextRipple = nextMobileTitleRipple(arrivalRef.current, rippleRef.current, attention);
  arrivalRef.current = nextRipple.arrival;
  rippleRef.current = nextRipple.ripple;
  return (
    <div class="mchrome">
      <button
        type="button"
        class={`mcap mcap-left${presentation.hasAttention ? ` has-attention mcap-attention-${presentation.tone}` : ""}`}
        onClick={() => onToggle?.(!open)}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={mobileSessionsDoorLabel(attention)}
      >
        <Menu size={18} aria-hidden="true" />
        {presentation.hasAttention && (
          <span key={rippleRef.current} class="mcap-badge" aria-hidden="true" />
        )}
      </button>
      <MobileTitleChip
        title={title}
        open={panelOpen}
        onToggle={onPanel}
        inboxCount={inboxCount}
      />
      <button
        type="button"
        class="mcap mcap-right"
        onClick={onNew}
        aria-label="New session"
      >
        <Plus size={18} aria-hidden="true" />
      </button>
    </div>
  );
}
