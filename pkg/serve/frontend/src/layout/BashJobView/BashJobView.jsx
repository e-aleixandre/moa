import { useEffect, useState } from "preact/hooks";
import { ArrowLeft, Terminal, Square, Copy, Check, X } from "lucide-preact";
import { Kbd, IconButton, Spinner } from "../../primitives/index.js";
import { BashJobLog } from "../../components/BashJobLog/BashJobLog.jsx";
import { bashJobView } from "../../data/bash-job-view-model.js";
import { copyToClipboard, sessionTitle } from "../../data/util/format.js";
import { cancelBashJob } from "../../data/session-actions.js";
import "./BashJobView.css";

// BashJobView — "inside the background job". The read-only sibling of
// SubagentView: same shape (breadcrumb header → subject card → body → terminal
// banner), same rebound rule, but there is no conversation to have with a
// process. So the only action is stopping it, and everything else is reading:
// the FULL command (the dock and the inline strip only ever show its first
// line), its cwd, and its output as it streams.
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
    <div class="bashjob-view" role="region" aria-label={`Background job, ${view.status}`}>
      <header class="bj-head">
        <IconButton label="Back to conversation" onClick={onBack}>
          <ArrowLeft size={15} />
        </IconButton>
        <div class="bj-crumb">
          <Terminal size={13} aria-hidden="true" />
          <button type="button" class="bj-crumb-parent" onClick={onBack}>
            {sessionTitle(session)}
          </button>
          <span class="bj-crumb-sep" aria-hidden="true">›</span>
          <span class="bj-crumb-name">bash</span>
        </div>
        <div class="bj-head-actions">
          <BjStatus view={view} />
          <StopButton view={view} armed={confirmStop} onStop={onStop} />
          <Kbd>esc</Kbd>
        </div>
      </header>

      <CommandCard view={view} />

      <BashJobLog lines={view.lines} hiddenLines={view.hiddenLines} live={!view.terminal} />

      {view.terminal && <BjOutcome view={view} onBack={onBack} />}
    </div>
  );
}

// BjStatus — the job's own state, worded like the dock: a live job breathes,
// a terminal one just names its end. It is not a StateDot: those speak the
// session's vocabulary (idle/running/permission), not a process's.
function BjStatus({ view }) {
  if (view.terminal) return <span class={`bj-status ${view.outcome}`}>{view.status}</span>;
  return (
    <span class="bj-status running">
      <Spinner color="blue" size={10} />
      {view.cancelling ? "stopping" : "running"}
    </span>
  );
}

// StopButton — the only action in the view. It disappears (rather than sitting
// disabled) once the job is terminal: a stopped process offers nothing, and a
// permanently greyed control just adds noise to a screen that is now history.
function StopButton({ view, armed, onStop }) {
  if (view.terminal) return null;
  if (!view.canCancel) {
    // 'cancelling': the stop was already accepted and the process is winding
    // down. Report that instead of offering to stop it twice.
    return <span class="bj-stopping">stopping…</span>;
  }
  return (
    <button
      type="button"
      class={`bj-stop${armed ? " armed" : ""}`}
      onClick={onStop}
      aria-label={armed ? "Confirm stopping the job" : "Stop the job"}
    >
      <Square size={11} fill="currentColor" aria-hidden="true" />
      {armed ? "sure?" : "stop"}
    </button>
  );
}

// CommandCard — the whole command, wrapped, plus its working directory. The
// dock, the ledger and the inline strip all show a single truncated line; this
// is the one place the command can be read in full (and copied).
function CommandCard({ view }) {
  const [copied, setCopied] = useState(false);
  const copy = () => {
    copyToClipboard(view.command).then((ok) => {
      if (!ok) return;
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    });
  };
  return (
    <div class="bj-cmd">
      <div class="bj-cmd-head">
        <span class="bj-cmd-label">COMMAND</span>
        {view.cwd && <span class="bj-cmd-cwd">{view.cwd}</span>}
        <button type="button" class="bj-cmd-copy" onClick={copy} aria-label="Copy command">
          {copied ? <Check size={12} /> : <Copy size={12} />}
          {copied ? "copied" : "copy"}
        </button>
      </div>
      <pre class="bj-cmd-body">{view.command || "(no command recorded)"}</pre>
    </div>
  );
}

// BjOutcome — terminal banner, same three ends and same colors as the
// subagent's. It replaces the stop button rather than the log: the output is
// the point of this screen and stays readable after the job ends, so a job
// that finishes while being watched settles in place instead of ejecting the
// reader back to the conversation.
function BjOutcome({ view, onBack }) {
  return (
    <div class={`bj-outcome ${view.outcome}`}>
      <div class="bj-outcome-head">
        {view.outcome === "completed" && (
          <span class="bj-outcome-check"><Check size={13} strokeWidth={2.5} /></span>
        )}
        {view.outcome === "failed" && <X size={15} aria-hidden="true" />}
        <b>{view.status}</b>
      </div>
      <button type="button" class="bj-outcome-back" onClick={onBack}>Back to conversation</button>
    </div>
  );
}
