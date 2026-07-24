import { Composer } from "../../Composer/Composer.jsx";
import { MobileStatusLine } from "../MobileStatusLine/MobileStatusLine.jsx";
import "./MobileComposer.css";

// MobileComposer — CONNECTED bottom input for the mobile conversation. It wraps
// the REAL, shared <Composer> (send / queue / slash / @-mention / attachments /
// stop) and, below it, the persistent mobile chrome (<MobileStatusLine>).
//
// The old MobileHeader + horizontal SessionStrip are gone; the status line is
// the only persistent chrome down here. It holds three single-scope doors —
// context ring + cost (→ Context & usage), the ModelPill (→ Model & thinking)
// and the permission chip (→ Permissions) — plus the per-run token heartbeat at the
// right. Sessions is NOT among them: that door is the floating title chip at the
// top of the screen (MobileTitleChip). Live activity is not in the line either:
// it lives in the ephemeral now-line rendered above this composer
// (MobileNowLine). Each door opens the approved bottom sheet (MobileSheet),
// which — per device feedback — covers the full mobile container (scrim + sheet
// flush to the bottom), so the composer is never left exposed under it.
//
// Visual fit is CSS-only (MobileComposer.css); the composer's own textarea uses
// --text-input (≥16px) so iOS never auto-zooms, and this wrapper keeps the
// bottom safe-area inset via the status line below it.
export function MobileComposer({ session, usage }) {
  return (
    <div class="mcomposer">
      <Composer sessionId={session.id} session={session} shortPlaceholder />
      <MobileStatusLine session={session} usage={usage} />
    </div>
  );
}
