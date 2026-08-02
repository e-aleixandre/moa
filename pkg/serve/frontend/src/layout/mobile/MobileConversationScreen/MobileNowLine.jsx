import { NowLine } from "../../NowLine/NowLine.jsx";
import "./MobileNowLine.css";

// MobileNowLine — the mobile dressing of the shared NowLine: same component,
// same data (activityPhase / activityText / formatElapsed), only the `mnowline`
// class prefix and this stylesheet are mobile-specific. Kept as a named wrapper
// because MobileSubagentView reuses the `.mnowline` rules verbatim for the
// branch's own line, and the mobile screen imports it by name.
export function MobileNowLine({ session }) {
  return <NowLine session={session} base="mnowline" />;
}
