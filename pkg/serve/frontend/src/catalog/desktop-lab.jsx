import { useLayoutEffect, useState } from "preact/hooks";
import "./desktop-lab.css";

// DesktopLab — the real desktop conversation, framed at a laptop width, so it
// can be reviewed from a phone. It does not restyle or restage the screen:
// the child IS ConversationScreen, store-connected, same CSS. A change to
// ChatHead or the status strip shows up here because there is no second copy
// to forget.
//
// The frame is 1280×800 (the desktop width the responsive lab already uses).
// On a narrower viewport the frame scales to fit; pinch-zoom on the page still
// works because this route is not mobile-locked.

export const DESKTOP_LAB_WIDTH = 1280;
export const DESKTOP_LAB_HEIGHT = 800;
const DESKTOP_LAB_GUTTER = 24;

// scaleForViewport is the only piece of math in this lab: how small a 1280px
// screen has to get to fit on the phone. Pure so a test can catch a scale that
// would overflow (or a scale of 1 that would leave the frame unreadably tiny).
export function scaleForViewport(viewportWidth, frameWidth = DESKTOP_LAB_WIDTH, gutter = DESKTOP_LAB_GUTTER) {
  if (!viewportWidth || viewportWidth <= gutter) return 0;
  return Math.min(1, (viewportWidth - gutter) / frameWidth);
}

function fitScale() {
  if (typeof window === "undefined") return 1;
  return scaleForViewport(window.innerWidth);
}

export function DesktopLab({ children }) {
  const [scale, setScale] = useState(fitScale);
  useLayoutEffect(() => {
    const onFit = () => setScale(fitScale());
    onFit();
    window.addEventListener("resize", onFit);
    window.visualViewport?.addEventListener("resize", onFit);
    return () => {
      window.removeEventListener("resize", onFit);
      window.visualViewport?.removeEventListener("resize", onFit);
    };
  }, []);

  return (
    <div class="desktop-lab">
      <p class="desktop-lab-note">
        Real desktop at {DESKTOP_LAB_WIDTH}×{DESKTOP_LAB_HEIGHT}. Same screen, same components.
      </p>
      <div
        class="desktop-lab-stage"
        style={{ height: `${DESKTOP_LAB_HEIGHT * scale}px` }}
      >
        <div
          class="desktop-lab-slot"
          style={{
            width: `${DESKTOP_LAB_WIDTH * scale}px`,
            height: `${DESKTOP_LAB_HEIGHT * scale}px`,
          }}
        >
          <div
            class="desktop-lab-frame"
            style={{ transform: `scale(${scale})` }}
          >
            {children}
          </div>
        </div>
      </div>
    </div>
  );
}
