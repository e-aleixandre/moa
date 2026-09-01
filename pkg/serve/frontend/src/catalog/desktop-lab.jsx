import { useLayoutEffect, useState } from "preact/hooks";
import "./desktop-lab.css";

// ScreenLab — a real product screen, framed at a chosen size, so it can be
// reviewed from a different density. It does not restyle the child: a change
// to ChatHead or the status strip shows up here because there is no second copy.

export const DESKTOP_LAB_WIDTH = 1280;
export const DESKTOP_LAB_HEIGHT = 800;
export const PHONE_LAB_WIDTH = 390;
export const PHONE_LAB_HEIGHT = 760;
const LAB_GUTTER = 24;

export function scaleForViewport(viewportWidth, frameWidth = DESKTOP_LAB_WIDTH, gutter = LAB_GUTTER) {
  if (!viewportWidth || viewportWidth <= gutter) return 0;
  return Math.min(1, (viewportWidth - gutter) / frameWidth);
}

// Unzoomed layout width. Pinch-zoom shrinks the visual viewport (and, on some
// browsers, innerWidth with it). Fitting to that width undoes the zoom: the
// chrome grows and the frame stays the same size. Prefer the larger of
// innerWidth and visualViewport.width × scale.
export function layoutWidth(innerWidth, visualWidth, visualScale) {
  const fromVisual = visualWidth && visualScale ? visualWidth * visualScale : 0;
  return Math.max(innerWidth || 0, fromVisual);
}

export function ScreenLab({ width, height, note, children }) {
  const fit = () => {
    if (typeof window === "undefined") return 1;
    const vv = window.visualViewport;
    return scaleForViewport(layoutWidth(window.innerWidth, vv?.width, vv?.scale), width);
  };
  const [scale, setScale] = useState(fit);
  useLayoutEffect(() => {
    const onFit = () => {
      const next = fit();
      setScale((prev) => (prev === next ? prev : next));
    };
    onFit();
    window.addEventListener("resize", onFit);
    window.addEventListener("orientationchange", onFit);
    return () => {
      window.removeEventListener("resize", onFit);
      window.removeEventListener("orientationchange", onFit);
    };
  }, [width]);

  return (
    <div class="desktop-lab">
      <p class="desktop-lab-note">{note}</p>
      <div class="desktop-lab-stage" style={{ height: `${height * scale}px` }}>
        <div
          class="desktop-lab-slot"
          style={{ width: `${width * scale}px`, height: `${height * scale}px` }}
        >
          <div class="desktop-lab-frame" style={{ width: `${width}px`, height: `${height}px`, transform: `scale(${scale})` }}>
            {children}
          </div>
        </div>
      </div>
    </div>
  );
}

export function DesktopLab({ children }) {
  return (
    <ScreenLab
      width={DESKTOP_LAB_WIDTH}
      height={DESKTOP_LAB_HEIGHT}
      note={`Real desktop at ${DESKTOP_LAB_WIDTH}×${DESKTOP_LAB_HEIGHT}. Same screen, same components.`}
    >
      {children}
    </ScreenLab>
  );
}

export function PhoneLab({ children }) {
  return (
    <ScreenLab
      width={PHONE_LAB_WIDTH}
      height={PHONE_LAB_HEIGHT}
      note={`Real phone at ${PHONE_LAB_WIDTH}×${PHONE_LAB_HEIGHT}. Same screen, same components.`}
    >
      {children}
    </ScreenLab>
  );
}
