import { useState, useEffect } from "preact/hooks";
import { fetchBranchPoints } from "../../data/session-actions.js";
import "./EarlierBranches.css";

// EarlierBranches — the way back into the timeline from an EMPTY conversation.
// Rewinding the first message leaves nothing on screen, and the timeline is
// otherwise reached through a user message's confirmation. It shows only when
// the branch is empty and the session still holds branch points: a brand-new
// session has nothing to go back to and shows nothing.
//
// "Empty" is only meaningful once the init snapshot has landed: before it, an
// empty transcript just means nothing has arrived, and asking then walks the
// whole tree of a long session on the server for a link that will not show.
export function shouldAskBranchPoints({ sessionId, empty, initReceived }) {
  return !!sessionId && !!empty && !!initReceived;
}

export function EarlierBranches({ sessionId, empty, initReceived, onOpen }) {
  const [count, setCount] = useState(0);
  useEffect(() => {
    setCount(0);
    if (!shouldAskBranchPoints({ sessionId, empty, initReceived })) return undefined;
    let cancelled = false;
    fetchBranchPoints(sessionId)
      .then((points) => { if (!cancelled) setCount(Array.isArray(points) ? points.length : 0); })
      .catch(() => {});
    return () => { cancelled = true; };
  }, [sessionId, empty, initReceived]);

  if (!empty || count === 0) return null;
  return (
    <div class="earlier-branches">
      <button type="button" class="earlier-branches-link" onClick={onOpen}>
        Earlier branches
      </button>
    </div>
  );
}
