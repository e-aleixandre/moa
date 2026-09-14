import { useEffect, useRef, useState } from "preact/hooks";
import { MOTION, prefersReducedMotion } from "../../hooks/motion.js";

// LiveSentence — the phrase in the live bar, and the two things it does while
// the agent works.
//
// 1. It SHIMMERS. A highlight sweeps the glyphs (background-clip: text on a
//    ::before carrying the same text) for as long as a phrase holds. The
//    elapsed counter proves the run is alive in numbers; this is the same fact
//    said in a way you take in without reading. It runs only while something
//    is actually running -- a shimmer on "Waiting for you" would be a lie.
//
// 2. It SWAPS. When the phrase changes both halves move: the outgoing line
//    rises and blurs out, the incoming one arrives from below, held back just
//    enough that they do not cross in the middle. The first attempt animated
//    only the arriving text, which is why the change stayed invisible -- the
//    old words vanished instantly and the new ones faded in over the gap.
//
// The outgoing copy is absolutely positioned over the incoming one, so the bar
// never changes height mid-swap.
export function LiveSentence({ text, shimmer = false, class: cls = "" }) {
  const [shown, setShown] = useState(text);
  const [leaving, setLeaving] = useState(null);
  // One flag per line, and BOTH are needed for the same reason: a line must
  // spend one frame in its starting state before it is given its end state,
  // or there is no change for the browser to interpolate.
  //
  // `entering` holds the incoming line below its place. `exiting` holds the
  // outgoing one where it already is -- and its absence is why the swap read
  // as half an animation: .is-exit was applied on the very first frame, but
  // .is-exit IS the finished state (risen, blurred, transparent), so the old
  // phrase was never at opacity 1 for even one frame. It did not leave; it
  // was simply gone, while the new phrase faded in over the gap it left.
  const [entering, setEntering] = useState(false);
  const [exiting, setExiting] = useState(false);
  const timers = useRef([]);

  useEffect(() => () => timers.current.forEach(clearTimeout), []);

  useEffect(() => {
    if (text === shown) return undefined;
    if (prefersReducedMotion()) { setShown(text); return undefined; }

    setLeaving(shown);
    setShown(text);
    setEntering(true);
    setExiting(false);

    // Release both lines on the next frame, once the starting classes have
    // actually landed in the DOM. Doing it in the same frame lets the browser
    // coalesce the two states and animate nothing.
    const raf = requestAnimationFrame(() => {
      setEntering(false);
      setExiting(true);
    });
    const done = setTimeout(() => { setLeaving(null); setExiting(false); }, MOTION.fast + SWAP_GAP);
    timers.current.push(done);
    return () => { cancelAnimationFrame(raf); clearTimeout(done); };
  }, [text, shown]);

  return (
    <span class={`live-sentence${cls ? ` ${cls}` : ""}`}>
      {leaving !== null && (
        <span
          class={`live-sentence-line is-leaving${exiting ? " is-exit" : ""}`}
          aria-hidden="true"
          data-text={leaving}
        >
          {leaving}
        </span>
      )}
      <span
        class={`live-sentence-line${entering ? " is-enter" : ""}${shimmer ? " is-shimmer" : ""}`}
        data-text={shown}
      >
        {shown}
      </span>
    </span>
  );
}

// Matches --live-swap-gap in LiveBar.css: the incoming line waits this long so
// the two do not overlap at full opacity.
const SWAP_GAP = 50;
