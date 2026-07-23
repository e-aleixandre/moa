import { Composer } from "../../Composer/Composer.jsx";
import { MobileStatusLine } from "../MobileStatusLine/MobileStatusLine.jsx";
import "./MobileComposer.css";

// MobileComposer — CONNECTED bottom input for the mobile conversation. It wraps
// the REAL, shared <Composer> (send / queue / slash / @-mention / attachments /
// stop) and, below it, the persistent mobile chrome (<MobileStatusLine>).
//
// STATUSLINE-EXPLICIT-SESSIONS / FOUR DOORS: the old MobileHeader + horizontal
// SessionStrip are gone; the status line is the ONLY persistent chrome. It holds
// four single-scope doors — context ring + cost (→ Context & usage), the
// ModelPill (→ This session), the permission chip (→ Permissions), and an
// explicit, always-visible Sessions control (→ the SessionDrawer, whose footer
// hosts global Settings) that also aggregates cross-session attention. Live
// activity is NOT in the line: it lives in the ephemeral now-line rendered above
// this composer (MobileNowLine). Each door opens the approved bottom sheet
// (MobileSheet), which — per device feedback — covers the full mobile container
// (scrim + sheet flush to the bottom, like the SessionDrawer), so the composer
// is never left exposed under it.
//
// Visual fit is CSS-only (MobileComposer.css); the composer's own textarea uses
// --text-input (≥16px) so iOS never auto-zooms, and this wrapper keeps the
// bottom safe-area inset via the status line below it.
export function MobileComposer({
  session,
  usage,
  attnCount = 0,
  onOpenSessions,
  onRewind,
  rewindDisabled = false,
}) {
  return (
    <div class="mcomposer">
      <Composer sessionId={session.id} session={session} shortPlaceholder />
      <MobileStatusLine
        session={session}
        usage={usage}
        attnCount={attnCount}
        onOpenSessions={onOpenSessions}
        onRewind={onRewind}
        rewindDisabled={rewindDisabled}
      />
    </div>
  );
}
