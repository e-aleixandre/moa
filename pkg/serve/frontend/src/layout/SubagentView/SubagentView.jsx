import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { Stream } from "../Stream/Stream.jsx";
import { Composer } from "../Composer/Composer.jsx";
import { WorkHead, StateWord, StopButton, CopyAction, Disclosure, RunDetails } from "../WorkChrome/WorkChrome.jsx";
import "../WorkChrome/WorkChrome.css";
import "../LiveBar/LiveBar.css";
import { subagentView, canPromote } from "../../data/subagent-view-model.js";
import { fmtCost } from "../../data/util/usage-pills.js";
import { fmtTokens, sessionTitle } from "../../data/util/format.js";
import { useStore } from "../../hooks/useStore.js";
import { cancelSubagent, promoteSubagent } from "../../data/session-actions.js";
import { updateSession } from "../../data/store.js";
import { useSubagentTranscript } from "../../hooks/useSubagentTranscript.js";
import "./SubagentView.css";

// SubagentView — a DELEGATED ERRAND, not a conversation with a header.
//
// While it lives, the screen follows the errand; when it ends, the screen IS
// its report. The internal conversation is the record of how it got there, not
// the identity of the screen. That fixes the hierarchy, and the hierarchy
// fixes the layout:
//
//   1  the errand       what you asked for. It is the title.
//   2  state or result  what it is doing, or what it concluded.
//   3  work log         the messages and tools that prove it.
//   4  run details      model, tokens, cost, ids.
//
// What the previous version did wrong: model / started / turns / tokens /
// spend rode a permanent strip above the transcript, answering none of the
// questions you arrive with, and the RESULT of a finished run sat in a banner
// at the very bottom, under the whole record. Both are audit, and audit moved
// to Run details at the foot; the result moved to the top.
//
// Running and terminal are the same object on the same route, so they share
// the head. They do NOT share the composition: a live errand opens at its live
// end and offers a steer composer; a finished one opens at the top with the
// report and folds the record away.
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
        sub={<SubHead view={view} />}
        state={<SubState view={view} />}
        actions={<SubagentActions view={view} onPromote={onPromote} onStop={onCancel} confirmCancel={confirmCancel} />}
      />

      {view.terminal
        ? <SubagentReport view={view} session={session} />
        : <SubagentLive view={view} session={session} jobId={jobId} onBack={onBack} onCancel={onCancel} confirmCancel={confirmCancel} />}
    </div>
  );
}

// SubHead — who ran the errand, as the line that truncates FIRST. The agent
// and its effort are provenance, not the subject: they answer "who did this",
// which you ask after you know what it was.
//
// Exported because the phone push (MobileSubagentView) mounts the same head:
// two surfaces reading the same errand must not describe it with two
// different sub-lines.
export function SubHead({ view }) {
  const bits = [view.model, view.thinking && view.thinking !== "off" ? view.thinking : null].filter(Boolean);
  if (bits.length === 0) return null;
  return (
    <>
      <span class="sa-ident-dot" style={{ background: `var(--${view.accent})` }} aria-hidden="true" />
      {bits.map((b, i) => (
        <>
          {i > 0 && <span class="wk-sub-sep" aria-hidden="true">·</span>}
          <span key={b}>{b}</span>
        </>
      ))}
    </>
  );
}

// SubState — the persistent state in the head. Completed and cancelled are
// NEUTRAL: in this system green means running, and a green tick on something
// that stopped teaches the eye that green is decoration.
//
// Exported with SubHead, and for the same reason: the state vocabulary is the
// errand's, not the desktop's.
export function SubState({ view }) {
  if (!view.terminal) {
    return <StateWord tone="running" word="Running" time={view.elapsed} />;
  }
  if (view.outcome === "failed") return <StateWord tone="failed" word="Failed" />;
  if (view.outcome === "cancelled") return <StateWord tone="neutral" word="Cancelled" />;
  return <StateWord tone="neutral" word="Completed" />;
}

// SubagentActions — what you can still DO to a running errand: send it to the
// background, or stop it. Terminal runs get nothing, because there is nothing
// left to act on. Shared with the phone so the two surfaces cannot end up
// offering different verbs for the same state; `phone` only grows the targets.
//
// The two buttons are ONE group, and that is structural rather than cosmetic:
// on a 390px head the row has to wrap, and a bare fragment let "Stop" break
// away from "to background" and land alone on the next line, reading as a
// second, unrelated control.
export function SubagentActions({ view, phone = false, onPromote, onStop, confirmCancel }) {
  if (view.terminal) return null;
  return (
    <span class="sa-acts">
      {canPromote(view) && (
        <button type="button" class={`sa-promote${phone ? " is-phone" : ""}`} onClick={onPromote}>
          to background
        </button>
      )}
      <StopButton phone={phone} armed={confirmCancel} onStop={onStop} />
    </span>
  );
}

// SubagentLive — direct. The record is the body and opens at its live end; the
// now-line and the steer composer are the foot. This is the functional reason
// the fork deserves a whole screen.
function SubagentLive({ view, session, jobId, onBack, onCancel, confirmCancel }) {
  return (
    <>
      <div class="sa-body">
        <Stream
          session={{ id: session.id, messages: [] }}
          blocks={view.blocks}
          waypointAccent={view.accent}
        />
      </div>

      {(view.action || view.elapsed) && (
        <div class="livebar">
          <div class="lb-bar">
            <div class="lb-now" role="status" aria-live="polite">
              <span class="lb-dot is-working" aria-hidden="true" />
              <span class="lb-txt">{view.action || "working"}</span>
              {view.elapsed && <span class="lb-el">{view.elapsed}</span>}
            </div>
          </div>
        </div>
      )}
      <Composer
        key={`steer-${jobId}`}
        sessionId={session.id}
        session={session}
        steer={{
          jobId,
          name: view.name,
          onRebound: onBack,
          onStop: onCancel,
          stopArmed: confirmCancel,
        }}
      />
    </>
  );
}

// SubagentReport — a finished run is not a banner stapled under a transcript.
// The outcome sentence and the result ARE the page and get the top of it; the
// record folds below; the audit closes it. Nothing is pinned to the bottom,
// because the report wins that space.
export function SubagentReport({ view, session, phone = false }) {
  const bodyRef = useRef(null);
  // Same route, opposite anchor: a report opens at the TOP. Without this the
  // shared scroller keeps the live end it was following when the run ended.
  useLayoutEffect(() => {
    if (bodyRef.current) bodyRef.current.scrollTop = 0;
  }, [view.jobId, view.outcome]);

  const usage = view.usage;
  const details = [];
  if (view.model) details.push(["Model", view.thinking && view.thinking !== "off" ? `${view.model} · ${view.thinking}` : view.model]);
  details.push(["Mode", view.async ? "background" : "sync"]);
  if (view.elapsed) details.push(["Duration", view.elapsed]);
  if (usage && (usage.inputTokens || usage.outputTokens)) {
    details.push(["Tokens", `↑${fmtTokens(usage.inputTokens || 0)} ↓${fmtTokens(usage.outputTokens || 0)}`]);
  }
  if (usage && usage.costUSD > 0) details.push(["Cost", fmtCost(usage.costUSD)]);
  if (Number.isInteger(view.actionCount)) details.push(["Actions", String(view.actionCount)]);

  const ids = [["Job ID", view.jobId]];
  if (session?.id) ids.push(["Parent session", session.id]);

  return (
    <div class={`sa-report-body${phone ? " is-phone" : ""}`} ref={bodyRef}>
      <div class="sa-report-col">
        <ReportHeadline view={view} />

        {view.outcome === "completed" && view.result && (
          <>
            <div class="sa-result">{view.result}</div>
            <div class="sa-report-acts">
              <CopyAction phone={phone} text={view.result} label="Copy result" />
            </div>
          </>
        )}

        {/* The error VERBATIM, and it is the result — not a summary of it,
            not the first four lines with the rest thrown away. A failure you
            cannot paste is a failure you cannot report. */}
        {view.outcome === "failed" && view.error && (
          <>
            <pre class="sa-error">{view.error}</pre>
            <div class="sa-report-acts">
              <CopyAction phone={phone} text={view.error} label="Copy error" />
            </div>
          </>
        )}

        {view.outcome === "cancelled" && (
          <p class="sa-cancelled">
            {view.lastProgress
              ? <>Last progress: it had run <span class="sa-data">{view.lastProgress}</span>. It reached no conclusion.</>
              : <>It was stopped before it recorded any work.</>}
          </p>
        )}

        <Disclosure
          label="Work log"
          count={Number.isInteger(view.actionCount) && view.actionCount > 0
            ? `${view.actionCount} ${view.actionCount === 1 ? "action" : "actions"}`
            : undefined}
        >
          <div class="sa-log">
            <Stream
              session={{ id: `${session?.id}:${view.jobId}`, messages: [] }}
              blocks={view.blocks}
              waypointAccent={view.accent}
            />
          </div>
        </Disclosure>

        <RunDetails phone={phone} rows={details} ids={ids} />
      </div>
    </div>
  );
}

// The headline sentence. The head above already carries the persistent state,
// so this does not repeat it with a glyph 100px lower: one state, one
// persistent place, one contextual expression.
function ReportHeadline({ view }) {
  const word = view.outcome === "failed" ? "Failed" : view.outcome === "cancelled" ? "Cancelled" : "Completed";
  const sentence = view.elapsed
    ? `${word} ${view.outcome === "completed" ? "in" : "after"} ${view.elapsed}`
    : word;
  return <h3 class={`sa-headline is-${view.outcome}`}>{sentence}</h3>;
}

// openSubagentSibling is the one place that switches which fork is on screen.
// Kept exported (rather than inlined) because the LiveBar row and the
// delegation row both reach the same state through it.
export function openSubagentSibling(sessionId, jobId) {
  updateSession(sessionId, { viewingSubagent: jobId });
}
