import { useRef } from "preact/hooks";
import {
  mobileSessionsDoorLabel,
  mobileTitleChipPresentation,
  nextMobileTitleRipple,
} from "../MobileConversationScreen/attention-model.js";
import { MobileTitleChip } from "../MobileTitleChip/MobileTitleChip.jsx";
import "./MobileChrome.css";

// MobileChrome — the phone's header: three capsules floating over the top of
// the transcript. Markup and CSS are the catalogue's (catalog/zones-lab.jsx
// `Phone` chrome, zones-lab.css the `.zl-chrome` / `.zl-cap` / `.zl-burger` /
// `.zl-cap-badge` block), MOVED here rather than imitated: the classes
// travelled with the rules, so the row IS the accepted design instead of a
// translation of it. The catalogue imports this component now, which is what
// makes one definition rather than two.
//
// What is NOT the catalogue's is everything the prototype never had, grafted
// on top: the sessions door opens the SessionDrawer that already exists, the
// right capsule opens that drawer on its create step, the middle capsule
// opens THIS session's panel (the dossier the desktop opens from its crumb),
// and the attention badge is conditional — yellow / red / mauve by the
// session that wants you, re-keyed by arrival so a new arrival restarts its
// finite ripple without looping. The catalogue always painted a yellow dot;
// production only mounts it when another session actually needs you.
//
// The badge lives on the sessions door: it says "another session wants you",
// so it belongs on the button that goes to them rather than on the name of
// the one you are already reading.

function PlusIcon() {
  return (
    <svg viewBox="0 0 16 16" aria-hidden="true">
      <path d="M8 3.5v9M3.5 8h9" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" />
    </svg>
  );
}

export function MobileChrome({
  title,
  attention = {},
  open = false,
  onToggle,
  panelOpen = false,
  onPanel,
  onNew,
  inboxCount = 0,
  // alert — THIS session's alarm, on the capsule that opens this session's
  // panel. Distinct from `attention`, which is about the OTHER sessions and
  // lives on the left capsule.
  alert = "",
}) {
  const presentation = mobileTitleChipPresentation(attention);
  const arrivalRef = useRef(0);
  const rippleRef = useRef(0);
  const nextRipple = nextMobileTitleRipple(arrivalRef.current, rippleRef.current, attention);
  arrivalRef.current = nextRipple.arrival;
  rippleRef.current = nextRipple.ripple;
  return (
    <div class="zl-chrome">
      <button
        type="button"
        class={`zl-cap zl-cap-left${presentation.hasAttention ? ` has-attention zl-cap-attention-${presentation.tone}` : ""}`}
        onClick={() => onToggle?.(!open)}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={mobileSessionsDoorLabel(attention)}
      >
        <span class="zl-burger" aria-hidden="true" />
        {presentation.hasAttention && (
          <span key={rippleRef.current} class="zl-cap-badge" aria-hidden="true" />
        )}
      </button>
      <MobileTitleChip
        title={title}
        open={panelOpen}
        onToggle={onPanel}
        inboxCount={inboxCount}
        alert={alert}
      />
      <button
        type="button"
        class="zl-cap zl-cap-right"
        onClick={onNew}
        aria-label="New session"
      >
        <PlusIcon />
      </button>
    </div>
  );
}
