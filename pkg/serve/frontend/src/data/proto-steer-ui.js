// TEMPORARY PROTOTYPE SWITCH (feat/steer-queue-default) — remove once the owner picks A or B; see removal note below.
//
// Two ways of offering the explicit "redirect" gesture are being compared on a
// real device before one is chosen:
//   A — a "Redirect now" button on the queued marker (QueuedTail), plus the
//       keyboard shortcut and the composer hint.
//   B — a "Redirect" button beside Send on hover/fine-pointer devices and a
//       long-press menu on Send everywhere (Composer), plus the shortcut and
//       the hint. The queued marker only shows state.
// Everything shared between them (the data layer, ⌘/Ctrl+⏎, the hint, the
// `redirecting` state on the marker) is NOT behind the switch and stays.
//
// How it is chosen: open the app with ?steer=a or ?steer=b. The choice is kept
// in localStorage, so it survives the reload and the next visit (the native
// app WebView has no easy way to edit the URL after the first load); with
// neither, A. A page reload is the switch — nothing reacts live.
//
// HOW TO REMOVE (after the owner picks a variant):
//   1. Delete this file and proto-steer-ui.test.js, and every
//      `import { steerUIVariant }` of it.
//   2. `grep -rn "steerUIVariant(" src` lists every branch. There are two:
//      - QueuedTail.jsx: the "Redirect now" button, shown when its `variant`
//        prop (default `steerUIVariant()`, a test seam) is "a". Keep the
//        button if A won (drop the prop and the condition); delete the
//        button, its CSS (.queued-redirect), its tests and the
//        `.has-redirect` narrow-width rule if B won. The "Redirect now" wiring
//        in ConversationStream.jsx (onRedirect) goes with it.
//      - Composer.jsx: the "Redirect" button and the Send long-press menu,
//        both behind `variantB` (= `steerUIVariant() === "b"`): the
//        onSend* long-press handlers, `longPressRef`, `sendMenuOpen`, the
//        <SendRedirectMenu>, and in Composer.css the .zl-redirect and
//        .zl-send-menu rules. Keep them if B won (drop the condition); if A
//        won delete them AND SendRedirectMenu.jsx, restore the plain Send
//        handlers, and remove the `longPress` event from
//        data/composer-send-button.js (+ its three tests).
//   3. Rebuild the bundle (npm run build) so pkg/serve/static matches.
const KEY = "moa.proto.steerUI";

function readVariant() {
  let asked = null;
  try {
    asked = new URLSearchParams(globalThis.location?.search || "").get("steer");
  } catch (_) { /* no location (tests, prerender) */ }
  if (asked === "a" || asked === "b") {
    // The URL wins for this load even when it cannot be remembered.
    try { globalThis.localStorage?.setItem(KEY, asked); } catch (_) { /* storage can be blocked (private mode) */ }
    return asked;
  }
  try {
    const stored = globalThis.localStorage?.getItem(KEY);
    if (stored === "a" || stored === "b") return stored;
  } catch (_) { /* blocked storage: fall through to A */ }
  return "a";
}

// Read on every call rather than memoised: it is a URL parse and one storage
// read, and not holding state keeps the switch trivially testable and
// removable. A reload is the switch because nothing re-renders on its own when
// the stored value changes, not because the value is cached.
export function steerUIVariant() {
  return readVariant();
}
