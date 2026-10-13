import { useState, useEffect } from "preact/hooks";
import { fetchBranchPoints } from "../../data/session-actions.js";
import "./EarlierBranches.css";

// EarlierBranches — the way back into the timeline from an EMPTY conversation.
// Rewinding the first message leaves nothing on screen, and the timeline is
// otherwise reached through a user message's confirmation. It shows only when
// the branch is empty and the session still holds branch points: a brand-new
// session has nothing to go back to and shows nothing.
export function EarlierBranches({ sessionId, empty, onOpen }) {
  const [count, setCount] = useState(0);
  useEffect(() => {
    setCount(0);
    if (!empty || !sessionId) return undefined;
    let cancelled = false;
    fetchBranchPoints(sessionId)
      .then((points) => { if (!cancelled) setCount(Array.isArray(points) ? points.length : 0); })
      .catch(() => {});
    return () => { cancelled = true; };
  }, [sessionId, empty]);

  if (!empty || count === 0) return null;
  return (
    <div class="earlier-branches">
      <button type="button" class="earlier-branches-link" onClick={onOpen}>
        Earlier branches
      </button>
    </div>
  );
}
