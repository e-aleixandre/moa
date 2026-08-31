// useSessionSkills — the session's user-invocable skills, for the slash menu.
//
// Fetched per session rather than bundled with the built-in command list: which
// skills exist is a property of the workspace on disk, and a skill created
// mid-session should appear without a reload.
import { useEffect, useState } from 'preact/hooks';
import { api } from '../data/api.js';

export function useSessionSkills(sessionId) {
  const [skills, setSkills] = useState([]);

  useEffect(() => {
    if (!sessionId) {
      setSkills([]);
      return;
    }
    let cancelled = false;
    api('GET', `/api/sessions/${sessionId}/skills`)
      .then((res) => {
        if (!cancelled) setSkills(res?.skills || []);
      })
      // The slash menu is useful without skills; a failure here must not break
      // the composer.
      .catch(() => {
        if (!cancelled) setSkills([]);
      });
    return () => { cancelled = true; };
  }, [sessionId]);

  return skills;
}
