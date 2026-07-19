import { RotateCcw, Bell } from "lucide-preact";
import { StateDot } from "../../../primitives/index.js";
import { ModelPill } from "../../../components/index.js";
import { modelAccent } from "../../../data/selectors.js";
import "./MobileHeader.css";

// MobileHeader — session header for the mobile conversation screen.
// A top-center handle (real <button aria-label="Open sessions">) sits above two
// dense rows: identity (state dot + title + model pill + bell) and a mono meta
// row (path · ctx · rewind). The dedicated grab/hint band is gone (MOBILE-
// POLISH-SPEC §3); its "open sessions" job is now honest — the handle both taps
// open and is the start of the swipe-down gesture (§4). `swipeBind` carries the
// touch handlers from the parent's useDrawerSwipe hook; the whole header is the
// gesture surface.
//
// `empty` variant (EMPTY-STATE-SPEC §2.1): when there is no focused session the
// header must stop impersonating one — it keeps the handle (tap + swipe still
// open the drawer) and the bell (device-wide setting), drops the state dot,
// model pill and the whole meta row, and shows the wordmark "moa" styled as
// chrome (subtext0), not as a session title.
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
  swipeBind,
  empty = false,
}) {
  const hasCtx = typeof ctx === "number" && ctx >= 0;
  return (
    <header class="mhead" {...swipeBind}>
      <button
        type="button"
        class="mhead-handle"
        aria-label="Open sessions"
        onClick={onOpenSessions}
      >
        <span class="mhead-handle-bar" aria-hidden="true" />
      </button>
      <div class="mhead-row">
        {empty ? (
          <span class="mhead-title mhead-wordmark">moa</span>
        ) : (
          <>
            <StateDot state={state} size={9} />
            <span class="mhead-title">{title}</span>
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
