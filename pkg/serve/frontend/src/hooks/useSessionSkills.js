// useSessionSkills — the session's user-invocable skills, for the slash menu.
//
// Fetched per session rather than bundled with the built-in command list: which
// skills exist is a property of the workspace on disk, so a skill created while
// the session is open has to be able to appear without a restart.
import { useCallback, useEffect, useRef, useState } from 'preact/hooks';
import { api } from '../data/api.js';

// How long a fetched list is considered current. Typing "/" refreshes the list,
// but repeated keystrokes must not each hit the server; a few seconds is short
// enough that a skill created moments ago shows up on the next attempt.
const SKILLS_TTL_MS = 5000;

export function useSessionSkills(sessionId) {
  const [skills, setSkills] = useState([]);
  const fetchedAt = useRef(0);
  const inFlight = useRef(false);

  const load = useCallback((force) => {
    if (!sessionId) return;
    if (inFlight.current) return;
    if (!force && Date.now() - fetchedAt.current < SKILLS_TTL_MS) return;
    inFlight.current = true;
    api('GET', `/api/sessions/${sessionId}/skills`)
      .then((res) => {
        fetchedAt.current = Date.now();
        setSkills(res?.skills || []);
      })
      // The slash menu is useful without skills; a failure here must not break
      // the composer.
      .catch(() => {})
      .finally(() => { inFlight.current = false; });
  }, [sessionId]);

  useEffect(() => {
    // Drop the previous session's skills immediately: showing them against a
    // different workspace would offer commands that do not exist there.
    setSkills([]);
    fetchedAt.current = 0;
    load(true);
  }, [sessionId, load]);

  return { skills, refreshSkills: load };
}
