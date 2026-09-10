import { useEffect, useState } from "preact/hooks";
import { BashConsole } from "../../BashJobView/BashJobView.jsx";
import { bashJobView } from "../../../data/bash-job-view-model.js";
import { cancelBashJob } from "../../../data/session-actions.js";
import { useEdgeSwipeBack } from "../../../hooks/useEdgeSwipeBack.js";
import "../../WorkChrome/WorkChrome.css";
import "../../BashJobView/BashJobView.css";
import "./MobileBashJobView.css";

// MobileBashJobView — the same console, pushed full-screen.
//
// It does not reimplement the screen: it mounts BashConsole with phone={true}
// and adds only what a phone owes it — the push, the edge-swipe back, and
// touch-sized controls (which the shared chrome already carries under
// is-phone). The previous version had its own header, its own COMMAND card,
// its own status vocabulary and its own outcome banner, which is how it came
// to say "background · bash · COMPLETED" where the desktop said "Exit 0".
//
// Reuses the pure bashJobView() projection and rebounds to the conversation
// when the job is gone from the store.

export function MobileBashJobView({ session, jobId, onBack }) {
  const view = bashJobView(session, jobId);

  // All hooks run on EVERY render regardless of `view` (rules of hooks); each
  // guards internally rather than an early `if (!view)`.
  useEffect(() => {
    if (!view && session && jobId) onBack?.();
  }, [view, session, jobId, onBack]);

  const [confirmStop, setConfirmStop] = useState(false);
  // Swipe from the left edge to go back, the way a pushed screen is dismissed
  // on a phone. The head's own way back remains the accessible path.
  const { screenRef, dragging, swipeBind } = useEdgeSwipeBack({ onBack });
  useEffect(() => {
    if (!confirmStop) return;
    const t = setTimeout(() => setConfirmStop(false), 2000);
    return () => clearTimeout(t);
  }, [confirmStop]);

  if (!view) return null;

  const onStop = () => {
    if (!confirmStop) { setConfirmStop(true); return; }
    setConfirmStop(false);
    cancelBashJob(session.id, jobId).catch(() => {});
  };

  return (
    <div class={dragging ? "mbj is-swiping" : "mbj"} ref={screenRef} {...swipeBind}>
      <BashConsole
        view={view}
        session={session}
        phone
        onBack={onBack}
        onStop={onStop}
        confirmStop={confirmStop}
      />
    </div>
  );
}
