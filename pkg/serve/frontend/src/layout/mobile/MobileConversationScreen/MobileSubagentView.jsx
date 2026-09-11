import { useEffect, useState } from "preact/hooks";
import { Composer } from "../../Composer/Composer.jsx";
import { MobileStream } from "./MobileStream.jsx";
import { WorkHead } from "../../WorkChrome/WorkChrome.jsx";
import {
  SubagentReport, SubHead, SubState, SubagentActions,
} from "../../SubagentView/SubagentView.jsx";
import { subagentView } from "../../../data/subagent-view-model.js";
import { sessionTitle } from "../../../data/util/format.js";
import { cancelSubagent, promoteSubagent } from "../../../data/session-actions.js";
import { useEdgeSwipeBack } from "../../../hooks/useEdgeSwipeBack.js";
import { useSubagentTranscript } from "../../../hooks/useSubagentTranscript.js";
import "../../WorkChrome/WorkChrome.css";
import "../../SubagentView/SubagentView.css";
// The live line above the composer reuses LiveBar's rules verbatim (same
// grammar, different subject), so its stylesheet has to be in the graph even
// though this screen doesn't render that component. The composer sits in the
// phone's own `.mcomposer` pill, whose sheet belongs to MobileComposer.
import "../../LiveBar/LiveBar.css";
import "../MobileComposer/MobileComposer.css";
import "./MobileSubagentView.css";

// MobileSubagentView — the same DELEGATED ERRAND as the desktop screen,
// pushed full-screen over the conversation.
//
// It no longer reimplements the screen: it mounts the shared head (WorkChrome)
// and, when the run has ended, the shared report (SubagentReport with
// phone={true}). What is left here is what a phone actually owes it — the
// push, the edge-swipe back, and the phone's own live foot (now-line +
// steer composer in the `.mcomposer` pill).
//
// What the previous version did wrong, and what changed with it:
//   - the title was the CODENAME ("subagent · changelog") in a chat-style
//     header bar. The title is the ERRAND now; the agent that ran it is
//     provenance and rides the sub-line, exactly as on the desktop.
//   - the RESULT of a finished run sat in a banner below the whole
//     transcript, so the one thing you opened the screen for was the last
//     thing you reached. A finished run now IS the report, at the top, with
//     the record folded under it and the audit at the foot.
//   - completed was green. Green means running in this system; completed and
//     cancelled are neutral.
//   - a permanent StatusStrip carried the child's context ring, spend, model
//     and effort under the composer — audit, on the screen you steer from,
//     none of it answerable there. The figures live in Run details, where the
//     desktop keeps them; nothing was lost, it moved.
//   - the run-mode chip and the "Subagent details" sheet were the phone's own
//     vocabulary for things the shared chrome says once: promote is a verb in
//     the head's actions, the rest is Run details.
//
// Reuses the pure subagentView() projection; rebounds to the parent when the
// subagent was pruned.

export function MobileSubagentView({ session, jobId, onBack }) {
  const view = subagentView(session, jobId);

  // Same backfill as the desktop view: see useSubagentTranscript.
  useSubagentTranscript(session?.id, jobId, session?.subagents?.[jobId]?.lifecycleUnverified);

  // All hooks run on EVERY render regardless of `view` (rules of hooks); each
  // one guards internally for a null view rather than an early `if (!view)`.
  useEffect(() => {
    if (!view && session && jobId) onBack?.();
  }, [view, session, jobId, onBack]);

  const [confirmCancel, setConfirmCancel] = useState(false);
  // Swipe from the left edge to go back, the way a pushed screen is dismissed
  // on a phone. The head's own way back remains the accessible path.
  const { screenRef, dragging, swipeBind } = useEdgeSwipeBack({ onBack });
  useEffect(() => {
    if (!confirmCancel) return;
    const t = setTimeout(() => setConfirmCancel(false), 2000);
    return () => clearTimeout(t);
  }, [confirmCancel]);

  // Activity clock: re-render once a second while live so the elapsed timer
  // (derived from startedAtMs in the view model) advances on its own.
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!view || view.terminal) return;
    const t = setInterval(() => setTick((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, [view?.terminal]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!view) return null;

  const onCancel = () => {
    if (!confirmCancel) { setConfirmCancel(true); return; }
    setConfirmCancel(false);
    cancelSubagent(session.id, jobId).catch(() => {});
  };
  const onPromote = () => { promoteSubagent(session.id, jobId).catch(() => {}); };

  return (
    <div class={dragging ? "msa is-swiping" : "msa"} ref={screenRef} {...swipeBind}>
      <WorkHead
        phone
        parent={sessionTitle(session)}
        onBack={onBack}
        title={view.name}
        sub={<SubHead view={view} />}
        state={<SubState view={view} />}
        actions={
          <SubagentActions
            view={view}
            phone
            onPromote={onPromote}
            onStop={onCancel}
            confirmCancel={confirmCancel}
          />
        }
      />

      {view.terminal ? (
        <SubagentReport view={view} session={session} phone />
      ) : (
        <MobileSubagentLive
          view={view}
          session={session}
          jobId={jobId}
          onBack={onBack}
          onCancel={onCancel}
          confirmCancel={confirmCancel}
        />
      )}
    </div>
  );
}

// MobileSubagentLive — a running errand, on a phone: the record is the body,
// the now-line and the steer composer are the foot. Same anatomy as the
// desktop, in the phone's own materials — the transcript is MobileStream (the
// phone's scroller and density) and the composer sits in the `.mcomposer`
// pill, so steering a child feels like typing in the parent.
function MobileSubagentLive({ view, session, jobId, onBack, onCancel, confirmCancel }) {
  return (
    <>
      <MobileStream
        session={{ id: `${session.id}:${jobId}`, messages: [] }}
        blocks={view.blocks}
        waypointAccent={view.accent}
      />

      <div class="mcomposer zl-dock msa-foot">
        <div class="livebar">
          <div class="lb-bar">
            <div class="lb-now" role="status" aria-live="polite">
              <span class="lb-dot is-working" aria-hidden="true" />
              <span class="lb-txt">{view.action || "working"}</span>
              {view.elapsed && <span class="lb-el">{view.elapsed}</span>}
            </div>
          </div>
        </div>
        <Composer
          key={`steer-${jobId}`}
          sessionId={session.id}
          session={session}
          compact
          steer={{
            jobId,
            name: view.name,
            onRebound: onBack,
            onStop: onCancel,
            stopArmed: confirmCancel,
          }}
        />
      </div>
    </>
  );
}
