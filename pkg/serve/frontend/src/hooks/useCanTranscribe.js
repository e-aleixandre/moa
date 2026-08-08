import { useState, useEffect } from 'preact/hooks';
import { setMuxSupport } from '../data/api.js';

// useCanTranscribe — whether the backend has speech-to-text configured.
//
// The answer is a property of the server, not of any one component, and it
// cannot change while the page is open, so it is fetched once and shared by
// every caller instead of each one hitting /api/capabilities on mount.
let cached = null;
let inFlight = null;
const subscribers = new Set();

function loadCapability() {
  if (cached !== null) return Promise.resolve(cached);
  if (!inFlight) {
    inFlight = fetch('/api/capabilities', { headers: { 'X-Moa-Request': '1' } })
      .then((r) => r.json())
      .then((caps) => {
        setMuxSupport(caps.ws_mux === 1);
        cached = !!caps.transcribe;
        return cached;
      })
      .catch(() => {
        // A failed probe means "no voice" for now, but must not be cached as a
        // permanent no: a later mount retries.
        inFlight = null;
        return false;
      })
      .then((value) => {
        subscribers.forEach((fn) => fn(value));
        return value;
      });
  }
  return inFlight;
}

export function useCanTranscribe() {
  const [can, setCan] = useState(cached ?? false);

  useEffect(() => {
    if (cached !== null) {
      setCan(cached);
      return undefined;
    }
    subscribers.add(setCan);
    loadCapability();
    return () => subscribers.delete(setCan);
  }, []);

  return can;
}

// Exposed for tests, which need each case to start from a clean slate.
export function __resetCanTranscribeCache() {
  cached = null;
  inFlight = null;
  subscribers.clear();
}
