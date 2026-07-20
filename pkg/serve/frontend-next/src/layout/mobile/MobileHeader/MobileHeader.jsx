import { RotateCcw, Bell, ChevronDown } from "lucide-preact";
import { StateDot } from "../../../primitives/index.js";
import { ModelPill } from "../../../components/index.js";
import { modelAccent } from "../../../data/selectors.js";
import "./MobileHeader.css";

// MobileHeader — session header for the mobile conversation screen.
// The title itself is the sessions switcher (DRAWER-OPENER-PROPOSALS §B): the
// identity block [state dot · title · ▾] is ONE button (aria-label "Switch
// session") that opens the sessions drawer — the chevron is the universal
// "tap to switch" signifier, sitting on the object you're changing, and it
// reclaims the ~32px the old leading icon cost. Below the identity row a mono
// meta row carries path · ctx · rewind. Closing the drawer is a swipe-down on
// the sheet itself, not on the header.
//
// `empty` variant (EMPTY-STATE-SPEC §2.1): with no focused session the header
// stops impersonating one — the switcher becomes the wordmark "moa ▾" (still
// the way into the drawer), the model pill and meta row drop, and only the
// bell (device-wide setting) remains beside it.
export function MobileHeader({
  state = "idle",
  title,
  model,
  level,
  path,
  ctx,
  onOpenSessions,
  onRewind,
  rewindDisabled = false,
  onNotifications,
  notifPopover,
  notifAnchorRef,
  onModelClick,
  empty = false,
}) {
  const hasCtx = typeof ctx === "number" && ctx >= 0;
  return (
    <header class="mhead">
      <div class="mhead-row">
        {empty ? (
          <button
            type="button"
            class="mhead-switch mhead-switch-empty"
            aria-label="Switch session"
            onClick={onOpenSessions}
          >
            <span class="mhead-title mhead-wordmark">moa</span>
            <ChevronDown size={14} class="mhead-chev" aria-hidden="true" />
          </button>
        ) : (
          <>
            <button
              type="button"
              class="mhead-switch"
              aria-label="Switch session"
              onClick={onOpenSessions}
            >
              <StateDot state={state} size={9} />
              <span class="mhead-title">{title}</span>
              <ChevronDown size={14} class="mhead-chev" aria-hidden="true" />
            </button>
            {model && (
              <ModelPill
                model={model}
                level={level}
                variant="glyph"
                accent={modelAccent(model)}
                onClick={onModelClick}
                aria-label="Model & thinking"
              />
            )}
          </>
        )}
        <div class="mhead-notif" ref={notifAnchorRef}>
          <button
            type="button"
            class="mhead-bell"
            aria-label="Notifications"
            onClick={onNotifications}
          >
            <Bell size={16} aria-hidden="true" />
          </button>
          {notifPopover}
        </div>
      </div>
      {!empty && (
        <div class="mhead-sub">
          <span class="mhead-meta">
            {path}
            {hasCtx ? ` · ctx ${ctx}%` : ""} ·{" "}
          </span>
          <button
            type="button"
            class="mhead-rewind"
            onClick={onRewind}
            disabled={rewindDisabled || !onRewind}
            aria-label="Rewind"
          >
            <RotateCcw size={11} aria-hidden="true" /> rewind
          </button>
        </div>
      )}
    </header>
  );
}
