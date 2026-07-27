import { useEffect, useRef, useState } from "preact/hooks";

// usePinchZoom — pinch/pan/double-tap zoom scoped to a single element.
//
// A PWA lets the user pinch the whole app, which zooms the chrome along with
// the thing they wanted to look at and leaves the layout offset afterwards.
// This keeps the gesture inside the element: the transform applies to the
// content, so the surrounding UI stays put.
//
// The transform is written straight to the DOM during a gesture — a pinch fires
// touchmove at screen rate, and re-rendering the viewer on every frame drops
// the gesture. State only carries `zoomed`, so callers can style the container
// (e.g. drop a max-height) without re-rendering mid-pinch.
//
// Returns refs to attach to the container and the content element.
//
// Those refs are callbacks, not useRef objects: the zoomed element mounts only
// once the file has loaded, long after this hook first runs, so a plain ref
// would still be null when the effect ran and — with nothing in its deps to
// change — the listeners would never be attached at all.

const MAX_SCALE = 8;
const DOUBLE_TAP_MS = 300;
const DOUBLE_TAP_SLOP = 30; // px between taps that still counts as a double tap
const DOUBLE_TAP_SCALE = 2.5;

function distance(a, b) {
  return Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY);
}

function midpoint(a, b) {
  return { x: (a.clientX + b.clientX) / 2, y: (a.clientY + b.clientY) / 2 };
}

export function usePinchZoom({ minScale = 1 } = {}) {
  const [container, setContainer] = useState(null);
  const [content, setContent] = useState(null);
  const [zoomed, setZoomed] = useState(false);

  // Live gesture state, deliberately outside preact state (see above).
  const tf = useRef({ scale: 1, x: 0, y: 0 });
  const gesture = useRef(null);
  const lastTap = useRef({ t: 0, x: 0, y: 0 });

  useEffect(() => {
    if (!container || !content) return undefined;

    const apply = () => {
      const { scale, x, y } = tf.current;
      content.style.transform = `translate3d(${x}px, ${y}px, 0) scale(${scale})`;
    };

    // Keep the content from being panned entirely off-screen: the translation
    // is bounded by the overflow the current scale actually produces.
    const clamp = () => {
      const t = tf.current;
      if (t.scale <= minScale) {
        t.x = 0;
        t.y = 0;
        return;
      }
      const rect = container.getBoundingClientRect();
      const overflowX = Math.max(0, (rect.width * t.scale - rect.width) / 2);
      const overflowY = Math.max(0, (rect.height * t.scale - rect.height) / 2);
      t.x = Math.min(overflowX, Math.max(-overflowX, t.x));
      t.y = Math.min(overflowY, Math.max(-overflowY, t.y));
    };

    const setScale = (next, originX, originY) => {
      const t = tf.current;
      const clamped = Math.min(MAX_SCALE, Math.max(minScale, next));
      if (clamped === t.scale) return;
      // Zoom about the gesture's focal point: the content position under the
      // fingers stays under the fingers.
      const rect = container.getBoundingClientRect();
      const cx = originX - rect.left - rect.width / 2;
      const cy = originY - rect.top - rect.height / 2;
      const ratio = clamped / t.scale;
      t.x = cx - (cx - t.x) * ratio;
      t.y = cy - (cy - t.y) * ratio;
      t.scale = clamped;
      clamp();
      apply();
      setZoomed(clamped > minScale);
    };

    const reset = () => {
      tf.current = { scale: minScale, x: 0, y: 0 };
      content.style.transition = "transform 200ms ease-out";
      apply();
      setZoomed(false);
      setTimeout(() => {
        if (content) content.style.transition = "";
      }, 220);
    };

    const onTouchStart = (e) => {
      if (e.touches.length === 2) {
        // Two fingers are unambiguously a pinch: take the gesture so the
        // browser does not scroll or page-zoom underneath it.
        e.preventDefault();
        gesture.current = {
          startDist: distance(e.touches[0], e.touches[1]),
          startScale: tf.current.scale,
          startMid: midpoint(e.touches[0], e.touches[1]),
          startX: tf.current.x,
          startY: tf.current.y,
        };
        content.style.transition = "";
      } else if (e.touches.length === 1 && tf.current.scale > minScale) {
        // One finger pans, but only while zoomed in — at rest the container
        // keeps its normal scrolling.
        gesture.current = {
          pan: true,
          startTouchX: e.touches[0].clientX,
          startTouchY: e.touches[0].clientY,
          startX: tf.current.x,
          startY: tf.current.y,
        };
        content.style.transition = "";
      }
    };

    const onTouchMove = (e) => {
      const g = gesture.current;
      if (!g) return;
      if (g.pan && e.touches.length === 1) {
        e.preventDefault();
        tf.current.x = g.startX + (e.touches[0].clientX - g.startTouchX);
        tf.current.y = g.startY + (e.touches[0].clientY - g.startTouchY);
        clamp();
        apply();
        return;
      }
      if (e.touches.length !== 2 || !g.startDist) return;
      e.preventDefault();
      const t = tf.current;
      const nextScale = Math.min(
        MAX_SCALE,
        Math.max(minScale, (g.startScale * distance(e.touches[0], e.touches[1])) / g.startDist)
      );
      const mid = midpoint(e.touches[0], e.touches[1]);
      const rect = container.getBoundingClientRect();
      const cx = g.startMid.x - rect.left - rect.width / 2;
      const cy = g.startMid.y - rect.top - rect.height / 2;
      const ratio = nextScale / g.startScale;
      // Track the focal point AND the fingers' drift, so a pinch that also
      // moves pans at the same time.
      t.x = cx - (cx - g.startX) * ratio + (mid.x - g.startMid.x);
      t.y = cy - (cy - g.startY) * ratio + (mid.y - g.startMid.y);
      t.scale = nextScale;
      clamp();
      apply();
    };

    const onTouchEnd = (e) => {
      if (e.touches.length === 0) gesture.current = null;
      else if (e.touches.length === 1 && gesture.current && !gesture.current.pan) {
        // Lifting one finger of a pinch continues as a pan rather than
        // freezing until every finger is up.
        gesture.current = {
          pan: true,
          startTouchX: e.touches[0].clientX,
          startTouchY: e.touches[0].clientY,
          startX: tf.current.x,
          startY: tf.current.y,
        };
      }
      const scaled = tf.current.scale > minScale;
      if (scaled !== zoomed) setZoomed(scaled);
    };

    // Double tap toggles between fit and a readable zoom, the gesture people
    // expect from a photo viewer. Tracked manually because dblclick does not
    // fire reliably on touch.
    const onTouchEndTap = (e) => {
      if (e.changedTouches.length !== 1 || gesture.current) return;
      const touch = e.changedTouches[0];
      const now = Date.now();
      const prev = lastTap.current;
      if (
        now - prev.t < DOUBLE_TAP_MS &&
        Math.hypot(touch.clientX - prev.x, touch.clientY - prev.y) < DOUBLE_TAP_SLOP
      ) {
        e.preventDefault();
        lastTap.current = { t: 0, x: 0, y: 0 };
        if (tf.current.scale > minScale) reset();
        else {
          content.style.transition = "transform 200ms ease-out";
          setScale(DOUBLE_TAP_SCALE, touch.clientX, touch.clientY);
          setTimeout(() => {
            if (content) content.style.transition = "";
          }, 220);
        }
        return;
      }
      lastTap.current = { t: now, x: touch.clientX, y: touch.clientY };
    };

    // Trackpad pinch and ctrl+wheel on desktop arrive as a wheel event.
    const onWheel = (e) => {
      if (!e.ctrlKey) return;
      e.preventDefault();
      setScale(tf.current.scale * (1 - e.deltaY * 0.01), e.clientX, e.clientY);
    };

    const onDoubleClick = (e) => {
      e.preventDefault();
      if (tf.current.scale > minScale) reset();
      else setScale(DOUBLE_TAP_SCALE, e.clientX, e.clientY);
    };

    // passive:false throughout — these handlers call preventDefault to keep the
    // browser from page-zooming or rubber-banding mid-gesture.
    const opts = { passive: false };
    container.addEventListener("touchstart", onTouchStart, opts);
    container.addEventListener("touchmove", onTouchMove, opts);
    container.addEventListener("touchend", onTouchEnd, opts);
    container.addEventListener("touchcancel", onTouchEnd, opts);
    container.addEventListener("touchend", onTouchEndTap, opts);
    container.addEventListener("wheel", onWheel, opts);
    container.addEventListener("dblclick", onDoubleClick);
    return () => {
      container.removeEventListener("touchstart", onTouchStart, opts);
      container.removeEventListener("touchmove", onTouchMove, opts);
      container.removeEventListener("touchend", onTouchEnd, opts);
      container.removeEventListener("touchcancel", onTouchEnd, opts);
      container.removeEventListener("touchend", onTouchEndTap, opts);
      container.removeEventListener("wheel", onWheel, opts);
      container.removeEventListener("dblclick", onDoubleClick);
    };
    // `zoomed` is read inside the handlers only to avoid redundant setState;
    // rebinding on every zoom change would tear down mid-gesture.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [container, content, minScale]);

  return { containerRef: setContainer, contentRef: setContent, zoomed };
}
