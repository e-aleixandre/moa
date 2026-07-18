import { MobileConversationScreen } from "../layout/index.js";
import "./mobile-gallery.css";

// MobileGallery — shows the MobileConversationScreen (sub-phase 4A) inside
// a realistic phone frame (notch, rounded corners, shadow). The
// layout is ready for two frames side by side: in 4B the
// second one with the sessions drawer will be added.
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
      </div>
    </div>
  );
}
