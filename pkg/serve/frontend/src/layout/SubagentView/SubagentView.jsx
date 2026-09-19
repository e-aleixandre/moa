import { useEffect, useState } from "preact/hooks";
import { Square } from "lucide-preact";
import { Stream } from "../Stream/Stream.jsx";
import { Composer } from "../Composer/Composer.jsx";
import { CtxRing, StatusStrip } from "../StatusStrip/StatusStrip.jsx";
import { WorkHead, StateWord, CopyAction, RunIds } from "../WorkChrome/WorkChrome.jsx";
import { Sheet } from "../../components/Sheet/Sheet.jsx";
import "../WorkChrome/WorkChrome.css";
import "../LiveBar/LiveBar.css";
import { subagentView, canPromote } from "../../data/subagent-view-model.js";
import { fmtCost } from "../../data/util/usage-pills.js";
import { fmtTokens, sessionTitle } from "../../data/util/format.js";
import { turnFinalResponse } from "../../data/stream-model.js";
import { useStore } from "../../hooks/useStore.js";
import { cancelSubagent, promoteSubagent } from "../../data/session-actions.js";
import { updateSession } from "../../data/store.js";
import { useSubagentTranscript } from "../../hooks/useSubagentTranscript.js";
import "./SubagentView.css";

// SubagentView — a DELEGATED ERRAND, not a conversation with a header.
//
// The screen is the errand's RECORD, in both its states. While it lives, the
// record is followed at its live end and you can steer it; when it ends, the
// same record is there, whole, and the run's figures become a foot. What
// changes between the two is the foot, not the page.
//
// The anatomy, top to bottom, is the same in both:
//
//   1  the head       ONE row: the way back, the errand, its state, and the
//                     one thing you can still do to it (send it to the
//                     background). It used to stack a second row with the
//                     title and the model under the first, which on a phone
//                     read as two headers for one screen.
//   2  the record     the conversation, at full width, never folded.
//   3  the foot       live: what it is doing, for how long, and Stop; the
//                     status strip keeps the model and thinking beside the
//                     composer. Ended: model, mode, duration, tokens, cost,
//                     and Copy.
//
// What earlier versions did wrong, and what each fix cost. A permanent strip
// of model/started/turns/tokens/spend above the transcript answered none of
// the questions you arrive with; it became "Run details", folded at the foot,
// which is where audit belongs — but folded is also where a record you came to
// read must never be, and the whole conversation went in with it. The fold is
// gone now: the figures are a bar, the record is the page.
//
// Props: { session, jobId, onBack }. onBack clears viewingSubagent.

export function SubagentView({ session, jobId, onBack }) {
  const view = subagentView(session, jobId);

  // A subagent born while this conversation was open holds only the deltas
  // received since; fetch the history that predates opening this view.
  useSubagentTranscript(session?.id, jobId, session?.subagents?.[jobId]?.lifecycleUnverified);

  // Rebound: the subagent was pruned (finished + cleaned). Fall back to parent.
  // All hooks below run on EVERY render regardless of `view` (rules of hooks);
  // each one guards internally for a null view instead of bailing out early.
  useEffect(() => {
    if (!view && session && jobId) onBack?.();
  }, [view, session, jobId, onBack]);

  // Cancel confirm-inline: first click arms ("sure?"), a 2s timeout disarms.
  const [confirmCancel, setConfirmCancel] = useState(false);
  useEffect(() => {
    if (!confirmCancel) return;
    const t = setTimeout(() => setConfirmCancel(false), 2000);
    return () => clearTimeout(t);
  }, [confirmCancel]);

  // Activity clock: re-render once a second while the subagent is still live so
  // the elapsed timer (derived from startedAtMs in the view model) advances on
  // its own, mirroring ConversationScreen's nowMs tick.
  const [, setTick] = useState(0);
  useEffect(() => {
    if (!view || view.terminal) return;
    const t = setInterval(() => setTick((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, [view?.terminal]); // eslint-disable-line react-hooks/exhaustive-deps

  // Esc goes back. The `esc` capsule that used to advertise it is gone: a
  // shortcut does not deserve permanent pixels next to the errand.
  //
  // `[`/`]` sibling cycling went with the sibling rail: the LiveBar already
  // carries awareness of the other running jobs, and a second lane inside this
  // screen duplicated it.
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

  const onCancel = () => {
    if (!confirmCancel) { setConfirmCancel(true); return; }
    setConfirmCancel(false);
    cancelSubagent(session.id, jobId).catch(() => {});
  };
  const onPromote = () => { promoteSubagent(session.id, jobId).catch(() => {}); };

  return (
    <div
      class="subagent-view"
      role="region"
      aria-label={`Subagent ${view.name}, ${view.status}`}
    >
      <WorkHead
        parent={sessionTitle(session)}
        onBack={onBack}
        title={view.name}
        inlineTitle
        state={view.terminal ? <SubState view={view} /> : null}
        actions={<SubagentActions view={view} onPromote={onPromote} />}
      />

      {view.terminal
        ? <SubagentReport view={view} session={session} />
        : <SubagentLive view={view} session={session} jobId={jobId} onBack={onBack} onStop={onCancel} confirmCancel={confirmCancel} />}
    </div>
  );
}

// SubIdent — who ran the errand: the identity dot and the model that carried
// it. Provenance lives with the model settings: in the live status strip and
// in the finished report foot. It never rides the live line, whose only job is
// to say what is happening now.
//
// Exported because the phone push mounts the same bands: two surfaces reading
// the same errand must not name its agent in two different ways.
export function SubIdent({ view }) {
  const bits = [view.model, view.thinking && view.thinking !== "off" ? view.thinking : null].filter(Boolean);
  if (bits.length === 0) return null;
  return (
    <span class="sa-ident">
      <span class="sa-ident-dot" style={{ background: `var(--${view.accent})` }} aria-hidden="true" />
      {bits.join(" · ")}
    </span>
  );
}

// SubState — the persistent state in the head. Completed and cancelled are
// NEUTRAL: in this system green means running, and a green tick on something
// that stopped teaches the eye that green is decoration.
//
// Exported with SubHead, and for the same reason: the state vocabulary is the
// errand's, not the desktop's.
export function SubState({ view }) {
  if (view.outcome === "failed") return <StateWord tone="failed" word="Failed" />;
  if (view.outcome === "cancelled") return <StateWord tone="neutral" word="Cancelled" />;
  return <StateWord tone="neutral" word="Completed" />;
}

// SubagentActions — what you can still DO to a running errand from the head:
// send it to the background. Terminal runs get nothing, because there is
// nothing left to act on. Shared with the phone so the two surfaces cannot end
// up offering different verbs for the same state; `phone` only grows the
// target.
//
// STOP IS NOT HERE ANY MORE. It sat in this group, and with a way back, a
// state word and two controls on one row, 390px had to wrap — which is what
// made the head read as two heads. It moved to the live bar above the
// composer, which is where the parent conversation has kept it since the
// composer gave it up (LiveBar.jsx): the row that says the agent is working
// is the row that offers to stop it. Same two-step confirmation, same 44px.
export function SubagentActions({ view, phone = false, onPromote }) {
  if (view.terminal || !canPromote(view)) return null;
  return (
    <button type="button" class={`sa-promote${phone ? " is-phone" : ""}`} onClick={onPromote}>
      to background
    </button>
  );
}

// SubagentLiveBar — the band between the record and the steer composer, on
// both surfaces. It says only what is happening now and for how long, then
// ends in Stop; model and thinking belong to the status strip below composer.
//
// It is `.zl-live` verbatim, the parent conversation's own bar, so stopping a
// child is the same gesture in the same place as stopping the parent.
export function SubagentLiveBar({ view, onStop, confirmCancel }) {
  if (!view.action && !view.elapsed && !onStop) return null;
  return (
    <div class="zl-live">
      <div class="zl-live-bar">
        <div class="zl-live-now" role="status" aria-live="polite">
          <span class="zl-live-dot is-working" aria-hidden="true" />
          <span class="zl-live-txt">{view.action || "working"}</span>
          {!!view.elapsed && <span class="zl-live-el zl-data">{view.elapsed}</span>}
        </div>
        {onStop && (
          <button
            type="button"
            class={`zl-live-stop${confirmCancel ? " is-armed" : ""}`}
            onClick={onStop}
            aria-label={confirmCancel ? "Confirm stop" : "Stop"}
            title={confirmCancel ? "Tap again to stop this subagent" : "Stop this subagent"}
          >
            <Square size={11} fill="currentColor" aria-hidden="true" />
            <span>{confirmCancel ? "sure?" : "Stop"}</span>
          </button>
        )}
      </div>
    </div>
  );
}

// SubagentLive — direct. The record is the body and opens at its live end; the
// live bar and the steer composer are the foot. This is the functional reason
// the fork deserves a whole screen.
function SubagentLive({ view, session, jobId, onBack, onStop, confirmCancel }) {
  return (
    <>
      <div class="sa-body">
        <Stream
          session={{ id: session.id, messages: [] }}
          blocks={view.blocks}
          waypointAccent={view.accent}
        />
      </div>

      <SubagentLiveBar view={view} onStop={onStop} confirmCancel={confirmCancel} />
      <Composer
        key={`steer-${jobId}`}
        sessionId={session.id}
        session={session}
        steer={{ jobId, name: view.name, onRebound: onBack }}
      />
      <SubagentStatusStrip view={view} />
    </>
  );
}

// The subagent inherits neither the parent's permission setting nor its usage
// gauges. Its strip is only the child configuration, in the same status-strip
// position the parent uses for configuration below the composer.
export function SubagentStatusStrip({ view, compact = false }) {
  if (!view.model && view.contextPercent < 0) return null;
  return (
    <StatusStrip
      compact={compact}
      ctxPercent={view.contextPercent >= 0 ? view.contextPercent : undefined}
      modelName={view.model}
      modelMark={`var(--${view.accent})`}
      thinking={view.thinking || "off"}
      showPermission={false}
      showTokens={false}
    />
  );
}

// Context uses StatusStrip's own ring and percent, never an estimate from the
// parent. While live it belongs in that strip as progress; once terminal the
// same frozen reading settles into the report foot with the other run figures.
export function SubagentContext({ view }) {
  if (view.contextPercent < 0) return null;
  return (
    <span class="sa-context" title={`Child context ${view.contextPercent}% used`}>
      <CtxRing pct={view.contextPercent} />
      <span class="zl-data zl-num">{view.contextPercent}<span class="zl-unit">%</span></span>
    </span>
  );
}

// SubagentReport — a finished run, direction A: the RECORD is the page, whole
// and unfolded, and the report is a foot.
//
// What this replaces, and why. The record used to live inside two closed
// disclosures ("Work log", "Run details"), so the conversation you opened the
// screen to read was two taps away and, once opened, indented inside a fold.
// The owner, after using it on his phone: "esa forma de ver la conversación
// como con desplegables no me gusta. No sé qué ganamos con esa vista."
// Nothing, was the answer.
//
// The figures did NOT lose their place — "es verdad que viene bien ese
// informe". They moved to a foot that is always on screen, so model, mode,
// duration, tokens and cost are answerable at any scroll position, which the
// disclosure could not do even while open.
//
// The record is the SAME Stream the live run mounts, with its own scroller, so
// a finished errand reads exactly like the conversation it was. That also
// settles where a report opens: at the end, like every transcript in this
// product, which puts the conclusion on screen on arrival instead of at the
// bottom of a long scroll. The record is above it, one swipe up, in the order
// it happened.
export function SubagentReport({ view, session, phone = false }) {
  return (
    <>
      <Stream
        session={{ id: `${session?.id}:${view.jobId}`, messages: [] }}
        blocks={view.blocks}
        waypointAccent={view.accent}
        dense={phone}
        visibleDone={phone ? 1 : undefined}
        tail={<ReportOutcome view={view} />}
      />
      <SubagentFoot view={view} session={session} phone={phone} />
    </>
  );
}

// ReportOutcome — the run's conclusion, appended to the record ONLY when the
// record does not already end with it.
//
// A child's answer normally is the last assistant turn, and then printing it
// again under the transcript would be the old bottom banner returning. But the
// view model also accepts a `result` the backend reported outside the
// transcript (subagent-view-model.js resultText), and a failure is often a
// tool's error rather than anything the child said. Those have nowhere else to
// appear, so they close the record here — verbatim, never summarised.
function ReportOutcome({ view }) {
  if (view.outcome === "completed") {
    if (!view.result || recordEndsWith(view.blocks, view.result)) return null;
    return <div class="sa-outcome"><div class="sa-result">{view.result}</div></div>;
  }
  if (view.outcome === "failed" && view.error) {
    return <div class="sa-outcome"><pre class="sa-error">{view.error}</pre></div>;
  }
  if (view.outcome === "cancelled") {
    return (
      <div class="sa-outcome">
        <p class="sa-cancelled">
          {view.lastProgress
            ? <>Last progress: it had run <span class="sa-data">{view.lastProgress}</span>. It reached no conclusion.</>
            : <>It was stopped before it recorded any work.</>}
        </p>
      </div>
    );
  }
  return null;
}

// recordEndsWith — does the child's own transcript already close with this
// text? Compared against the last turn's final response, which is the same
// string the turn's own copy button puts on the clipboard.
function recordEndsWith(blocks, text) {
  const want = String(text || "").trim();
  if (!want) return true;
  for (let i = blocks.length - 1; i >= 0; i--) {
    const b = blocks[i];
    if (b.kind !== "document" && b.kind !== "streaming") continue;
    return String(turnFinalResponse(b.blocks) || "").trim() === want;
  }
  return false;
}

// SubagentFoot — the report, compacted to the figures that answer "what did
// this cost me", on a bar the record scrolls under. It is furniture at the
// bottom edge, in the same place the parent conversation keeps its own live
// bar, and it never scrolls away.
//
// The two IDENTIFIERS are not on it and could not be: a job id is 20 mono
// characters that would push the cost off a 390px screen, and it is the one
// datum here you never read — you copy it, once, into a terminal. So the
// figures themselves are the door: a tap opens the identifiers. They are one
// gesture away rather than gone, which is the trade this direction accepted.
function SubagentFoot({ view, session, phone }) {
  const [openIds, setOpenIds] = useState(false);
  const usage = view.usage;

  const marks = [];
  marks.push({ k: view.async ? "background" : "sync" });
  if (view.elapsed) marks.push({ k: view.elapsed, mono: true });
  if (usage && (usage.inputTokens || usage.outputTokens)) {
    marks.push({ k: `↑${fmtTokens(usage.inputTokens || 0)} ↓${fmtTokens(usage.outputTokens || 0)}`, mono: true });
  }
  if (usage && usage.costUSD > 0) marks.push({ k: fmtCost(usage.costUSD), mono: true });

  const ids = [["Job ID", view.jobId]];
  if (session?.id) ids.push(["Parent session", session.id]);

  const copy = view.outcome === "failed"
    ? { text: view.error || "", label: "Copy error" }
    : { text: view.result || "", label: "Copy result" };

  return (
    <div class={`sa-foot${phone ? " is-phone" : ""}`}>
      <button
        type="button"
        class={`sa-marks${phone ? " is-phone" : ""}`}
        onClick={() => setOpenIds(true)}
        aria-label="Run identifiers"
      >
        <SubIdent view={view} />
        <SubagentContext view={view} />
        {marks.map((m) => (
          <span class={`sa-mark${m.mono ? " zl-data" : ""}`} key={m.k}>{m.k}</span>
        ))}
      </button>
      {copy.text && <CopyAction phone={phone} text={copy.text} label={copy.label} />}
      <Sheet
        open={openIds}
        onClose={() => setOpenIds(false)}
        title="This run"
        ariaLabel="Run identifiers"
      >
        <RunIds ids={ids} phone={phone} />
      </Sheet>
    </div>
  );
}

// openSubagentSibling is the one place that switches which fork is on screen.
// Kept exported (rather than inlined) because the LiveBar row and the
// delegation row both reach the same state through it.
export function openSubagentSibling(sessionId, jobId) {
  updateSession(sessionId, { viewingSubagent: jobId });
}
