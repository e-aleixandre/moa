import { useEffect, useState } from "preact/hooks";
import { ChevronLeft, Terminal, Square, Check, X } from "lucide-preact";
import { Spinner } from "../../../primitives/index.js";
import { BashJobLog } from "../../../components/BashJobLog/BashJobLog.jsx";
import { bashJobView } from "../../../data/bash-job-view-model.js";
import { cancelBashJob } from "../../../data/session-actions.js";
import { useEdgeSwipeBack } from "../../../hooks/useEdgeSwipeBack.js";
import "./MobileBashJobView.css";

// MobileBashJobView — full-screen push counterpart of the desktop
// BashJobView, exactly as MobileSubagentView is of SubagentView: same
// projection, same anatomy (header → command card → log → outcome), phone
// chrome. There is no composer and no status line here, because there is
// nothing to say to a process and none of the session's figures belong to it:
// what a background job is, is a command and its output.
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
  // on a phone. The header chevron remains the accessible path.
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
      <header class="mbj-head">
        <button type="button" class="mbj-back" aria-label="Back to conversation" onClick={onBack}>
          <ChevronLeft size={20} />
        </button>
        <span class="mbj-ident">
          <Terminal size={13} aria-hidden="true" />
          <span class="mbj-kind">background</span>
          <span class="mbj-name">bash</span>
        </span>
        {view.terminal ? (
          <span class={`mbj-status ${view.outcome}`}>{view.status}</span>
        ) : view.canCancel ? (
          <button
            type="button"
            class={`mbj-stop${confirmStop ? " armed" : ""}`}
            onClick={onStop}
            aria-label={confirmStop ? "Confirm stopping the job" : "Stop the job"}
          >
            <Square size={11} fill="currentColor" aria-hidden="true" />
            {confirmStop ? "sure?" : "stop"}
          </button>
        ) : (
          <span class="mbj-status running">
            <Spinner color="blue" size={10} />
            stopping
          </span>
        )}
      </header>

      <div class="mbj-cmd">
        <div class="mbj-cmd-label">COMMAND</div>
        <pre class="mbj-cmd-body">{view.command || "(no command recorded)"}</pre>
        {view.cwd && <div class="mbj-cmd-cwd">{view.cwd}</div>}
      </div>

      <BashJobLog lines={view.lines} hiddenLines={view.hiddenLines} live={!view.terminal} />

      {/* The job ending while you watch settles into a banner under the log;
          it never navigates away, because the output you came to read is still
          on screen and is now final. */}
      {view.terminal && (
        <div class={`mbj-outcome ${view.outcome}`}>
          {view.outcome === "completed" && (
            <span class="mbj-outcome-check"><Check size={13} strokeWidth={2.5} /></span>
          )}
          {view.outcome === "failed" && <X size={15} aria-hidden="true" />}
          <b>{view.status}</b>
          <button type="button" class="mbj-outcome-back" onClick={onBack}>Back</button>
        </div>
      )}
    </div>
  );
}
