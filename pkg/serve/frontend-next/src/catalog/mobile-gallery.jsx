import {
  MobileConversationScreen,
  SessionDrawer,
} from "../layout/index.js";
import { DRAWER_SESSIONS } from "../layout/mobile/MobileConversationScreen/MobileConversationScreen.jsx";
import "./mobile-gallery.css";

const noop = () => {};

// DrawerSpecimen — the conversation screen with the sessions drawer forced
// open on top of it, so the gallery can show the bottom-sheet statically.
// The drawer anchors to `.mgal-screen` (position: relative), staying inside
// the phone frame rather than the window.
function DrawerSpecimen() {
  return (
    <>
      <MobileConversationScreen />
      <SessionDrawer
        open
        onClose={noop}
        sessions={DRAWER_SESSIONS}
        activeCount={4}
        savedCount={2}
        onSelect={noop}
        onNew={noop}
        onEdit={noop}
      />
    </>
  );
}

// MobileGallery — shows the MobileConversationScreen (sub-phase 4A) and the
// sessions drawer (sub-phase 4B) inside realistic phone frames (notch,
// rounded corners, shadow) laid out side by side.
export function MobileGallery() {
  return (
    <div class="mgal">
      <header class="mgal-head">
        <h1>
          moa studio · <em>mobile</em>
        </h1>
        <p>
          The full-screen conversation on the phone: session header, session
          strip, touch stream with a 3-level ledger, and an anti-zoom composer.
          Everything breathes on its own.
        </p>
      </header>

      <div class="mgal-frames">
        <figure class="mgal-figure">
          <div class="mgal-device">
            <span class="mgal-notch" aria-hidden="true" />
            <div class="mgal-screen">
              <MobileConversationScreen />
            </div>
          </div>
          <figcaption class="mgal-caption">
            Conversation — full-screen session
          </figcaption>
        </figure>

        <figure class="mgal-figure">
          <div class="mgal-device">
            <span class="mgal-notch" aria-hidden="true" />
            <div class="mgal-screen">
              <DrawerSpecimen />
            </div>
          </div>
          <figcaption class="mgal-caption">
            Overview drawer — pull down on header
          </figcaption>
        </figure>
      </div>
    </div>
  );
}
