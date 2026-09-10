import { useEffect, useState } from "preact/hooks";
import { BashJobLog } from "../../components/BashJobLog/BashJobLog.jsx";
import { WorkHead, StateWord, StopButton, CopyAction, RunDetails } from "../WorkChrome/WorkChrome.jsx";
import "../WorkChrome/WorkChrome.css";
import { bashJobView } from "../../data/bash-job-view-model.js";
import { sessionTitle } from "../../data/util/format.js";
import { cancelBashJob } from "../../data/session-actions.js";
import "./BashJobView.css";

// BashJobView — a CONSOLE for one process. Not a subagent, and not a chat.
//
// A background command has exactly two things worth a screen, and the screen is
// made of them and almost nothing else:
//
//   1  the command   its identity. Verbatim, mono, whole, copyable.
//   2  the output    what you came to read. Mono, scrolling, complete enough
//                    to paste.
//   3  run details   cwd and identifiers, folded, at the foot, where audit
//                    belongs.
//
// What the previous version did wrong: it copied SubagentView's shape
// (breadcrumb → subject card → body → terminal banner) onto something that is
// not an errand. The command sat in a labelled card under the header — a
// second header, competing with the first — and the end of the job was
// announced by a coloured banner glued below the log, so the news arrived
// under the thing you were reading and the state was said twice.
//
// Now the state is in the head, once, in the words a process actually ends
// with: an exit code (0 neutral, non-zero red), Cancelled (neutral grey),
// Failed (red — timeout or exec error, which have no code). Running is the
// only green thing here.
//
// There is deliberately NO cost and NO token count: a bash job spends neither,
// and a "$0.00" would be a claim about work this screen does not do.
//
// Data comes from the pure bashJobView(session, jobId) projection, which reads
// the same store entry ws-handlers keeps for the job — so a reconnect (which
// rebuilds that entry from the init snapshot's bash_jobs, output included)
// repaints this view without any special casing here.
//
// Props: { session, jobId, onBack }. onBack clears viewingBashJob.

export function BashJobView({ session, jobId, onBack }) {
  const view = bashJobView(session, jobId);

  // Rebound: the job is gone from the store (pruned after a reconnect that no
  // longer lists it). All hooks below run on EVERY render regardless of `view`
  // (rules of hooks), each guarding internally instead of bailing out early.
  useEffect(() => {
    if (!view && session && jobId) onBack?.();
  }, [view, session, jobId, onBack]);

  // Stop confirm-inline: first click arms ("sure?"), a 2s timeout disarms —
  // the same two-step SubagentView uses for cancel, because killing a build
  // halfway is as destructive there as here.
  const [confirmStop, setConfirmStop] = useState(false);
  useEffect(() => {
    if (!confirmStop) return;
    const t = setTimeout(() => setConfirmStop(false), 2000);
    return () => clearTimeout(t);
  }, [confirmStop]);

  useEffect(() => {
    if (!view) return;
    const onKey = (e) => {
      if (e.key !== "Escape") return;
      e.preventDefault();
      onBack?.();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [view, onBack]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!view) return null;

  const onStop = () => {
    if (!confirmStop) { setConfirmStop(true); return; }
    setConfirmStop(false);
    // The kill is echoed as bash_job_end, which flips the status in the store;
    // the toast on refusal is cancelBashJob's, so the rejection is swallowed.
    cancelBashJob(session.id, jobId).catch(() => {});
  };

  return (
    <div class="bashjob-view" role="region" aria-label={`Background command, ${view.status}`}>
      <BashConsole
        view={view}
        session={session}
        onBack={onBack}
        onStop={onStop}
        confirmStop={confirmStop}
      />
    </div>
  );
}

// BashConsole — the console itself, shared verbatim by the desktop view and
// the phone push (MobileBashJobView), so the two cannot drift into two
// different vocabularies for the same process. The phone only adds its own
// frame around this: a full-screen push and the edge-swipe back.
export function BashConsole({ view, session, phone = false, onBack, onStop, confirmStop }) {
  const total = view.lines.length + view.hiddenLines;
  const rows = [];
  // Only when the system actually observed one. A killed process and a failed
  // exec have no exit code, and neither has a live one. The line count is NOT
  // repeated here: it already rides the output's own header, six inches up.
  if (view.exitCode !== null) rows.push(["Exit code", String(view.exitCode)]);

  // The identifiers are copy targets, which is what you do with a path and a
  // job id: paste them somewhere else. A cwd in the printed rows would be
  // truncated with an ellipsis; here it is takeable.
  const ids = [];
  if (view.cwd) ids.push(["Working directory", view.cwd]);
  ids.push(["Job ID", view.jobId]);
  if (session?.id) ids.push(["Parent session", session.id]);

  return (
    <>
      {/* The command IS the title, and it is the copy target: it is the one
          thing you take away from this screen verbatim, and a 14px icon beside
          80 characters of shell is the worse of the two targets. */}
      <WorkHead
        phone={phone}
        parent={sessionTitle(session)}
        onBack={onBack}
        title={view.command || "(no command recorded)"}
        titleMono
        titleScroll
        copyLabel="Copy command"
        state={<BashState view={view} />}
        actions={
          view.terminal ? null : (
            <StopButton
              phone={phone}
              armed={confirmStop}
              onStop={onStop}
              busyLabel={view.canCancel ? undefined : "stopping…"}
            />
          )
        }
      />

      <div class={`bj-out${phone ? " is-phone" : ""}`}>
        <div class="bj-out-head">
          <span class="bj-out-label">Output</span>
          {total > 0 && (
            <span class="bj-out-n">{total.toLocaleString()} {total === 1 ? "line" : "lines"}</span>
          )}
          <span class="bj-out-sp" />
          {view.output && <CopyAction phone={phone} text={view.output} label="Copy output" />}
        </div>
        <BashJobLog lines={view.lines} hiddenLines={view.hiddenLines} live={!view.terminal} />
      </div>

      {/* The audit, folded, at the foot. A finished job settles in place: no
          banner is pushed under the log, because the head already says how it
          ended and the output you were reading is now the final one. */}
      <div class={`bj-foot${phone ? " is-phone" : ""}`}>
        <RunDetails phone={phone} rows={rows} ids={ids} />
      </div>
    </>
  );
}

// BashState — how a process ends, said the way a process ends it. Green is
// only ever "still running"; an exit 0 is neutral because a command that
// finished is not news, and the thing you scan a console for is the one that
// did not. 'Failed' (no code) is a timeout or an exec error: the reason is in
// the output, and the screen does not invent a number for it.
function BashState({ view }) {
  if (!view.terminal) {
    return <StateWord tone="running" word={view.cancelling ? "Stopping" : "Running"} />;
  }
  if (view.outcome === "cancelled") return <StateWord tone="neutral" word="Cancelled" />;
  if (view.outcome === "failed") return <StateWord tone="failed" word="Failed" />;
  if (view.exitCode === 0) return <StateWord tone="neutral" word="Exit 0" />;
  return <StateWord tone="failed" word={`Exit ${view.exitCode}`} />;
}
