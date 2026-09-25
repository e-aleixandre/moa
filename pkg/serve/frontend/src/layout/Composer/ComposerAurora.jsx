import { useEffect, useRef } from 'preact/hooks';
import { prefersReducedMotion } from '../../hooks/motion.js';
import { startAuroraWash } from './aurora-wash.js';

// The composer draws the wash only while YOUR voice is live: dictating, or a
// call that is actually on. Connecting and hanging up are not voice yet.
export function auroraActive({ recording, callPhase }) {
  return !!recording || callPhase === 'live';
}

const CLOUDS = [0, 1, 2, 3, 4];

/**
 * ComposerAurora — soft colour clouds across the lower half of the composer,
 * rising and brightening with the voice. Mounted only while auroraActive();
 * unmounting tears down the loop, the observers and the level meter.
 * getStream returns the live microphone of whichever capture is running.
 */
export function ComposerAurora({ getStream }) {
  const elRef = useRef(null);
  const bodyRef = useRef(null);
  const getStreamRef = useRef(getStream);
  getStreamRef.current = getStream;

  useEffect(() => startAuroraWash(elRef.current, bodyRef.current, {
    getStream: () => getStreamRef.current?.(),
    reduced: prefersReducedMotion(),
  }), []);

  return (
    <div class="zl-aurora-wash" ref={elRef} aria-hidden="true">
      <div class="zl-aurora-wash-body" ref={bodyRef}>
        {CLOUDS.map((i) => <span key={i} class={`zl-aurora-cloud c${i}`} />)}
      </div>
    </div>
  );
}
