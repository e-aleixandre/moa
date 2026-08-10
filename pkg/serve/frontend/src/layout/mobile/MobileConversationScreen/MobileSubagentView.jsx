import { useEffect, useState } from "preact/hooks";
import { ChevronLeft, GitFork, X, Check, Copy } from "lucide-preact";
import { RunModeChip } from "../../../components/index.js";
import { Composer } from "../../Composer/Composer.jsx";
import { StatusLineRow } from "../MobileStatusLine/StatusLineRow.jsx";
import { MobileStream } from "./MobileStream.jsx";
import { subagentView, canPromote } from "../../../data/subagent-view-model.js";
import { fmtTokens, copyToClipboard, sessionTitle } from "../../../data/util/format.js";
import { fmtCost } from "../../../data/util/usage-pills.js";
import { modelAccent } from "../../../data/selectors.js";
import { cancelSubagent, promoteSubagent } from "../../../data/session-actions.js";
// The now-line above the composer reuses MobileNowLine's rules verbatim (same
// grammar, different subject), so its stylesheet has to be in the graph even
// though this screen doesn't render that component.
import "./MobileNowLine.css";
import "./MobileSubagentView.css";

// MobileSubagentView — full-screen push counterpart of the desktop SubagentView
//. It is the parent screen with a different subject: same now-line
// above the composer, same composer pill, same status line below it
// (StatusLineRow) — except every number in that line is the CHILD's (its own
// context window, spend, model, effort and tokens), because a screen zoomed
// into a branch that reported the trunk's figures would be describing something
// you can't see. Nothing in the line is a door: a child's model or effort isn't
// changed from inside it.
//
// What stays fork-proper: the full-screen push, a one-row header with the
// codename in accent and the way back, the run-mode chip, the sibling rail, the
// terminal outcome banner. The header survives even though
// the parent screen no longer has one — it is what says "you are one level in,
// and here is the way out".
//
// Reuses the pure subagentView() projection; rebounds to the parent when the
// subagent was pruned.

export function MobileSubagentView({ session, jobId, onBack }) {
  const view = subagentView(session, jobId);

  // All hooks run on EVERY render regardless of `view` (rules of hooks); each
  // one guards internally for a null view rather than an early `if (!view)`.
  useEffect(() => {
    if (!view && session && jobId) onBack?.();
  }, [view, session, jobId, onBack]);

  const [confirmCancel, setConfirmCancel] = useState(false);
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

  const accent = view.accent;

  const onCancel = () => {
    if (!confirmCancel) { setConfirmCancel(true); return; }
    setConfirmCancel(false);
    cancelSubagent(session.id, jobId).catch(() => {});
  };
  const onPromote = () => { promoteSubagent(session.id, jobId).catch(() => {}); };

  return (
    <div class="msa">
      <header class="msa-head">
        <button type="button" class="msa-back" aria-label={`Back to ${sessionTitle(session)}`} onClick={onBack}>
          <ChevronLeft size={20} />
        </button>
        <span class="msa-ident">
          <GitFork size={13} style={{ color: `var(--${accent})` }} aria-hidden="true" />
          <span class="msa-kind">subagent</span>
          <span class="msa-name" style={{ color: `var(--${accent})` }}>{view.name}</span>
        </span>
        <RunModeChip async={view.async} canPromote={canPromote(view)} onPromote={onPromote} />
      </header>

      <MobileStream
        session={{ id: `${session.id}:${jobId}`, messages: [] }}
        blocks={view.blocks}
        waypointAccent={accent}
      />

      {view.terminal && <MobileOutcome view={view} onBack={onBack} />}

      {/* Same anatomy as the parent screen: the ephemeral activity line above
          the composer, the composer, and — always, terminal or not — the status
          line pinned below. Only the subject changes. */}
      <div class="mcomposer msa-foot">
        {!view.terminal && (
          <>
            <div class="mnowline" role="status" aria-live="polite">
              <span class="mnowline-act">
                <span class="txt is-live">{view.action || "working"}</span>
              </span>
              {view.elapsed && <span class="mnowline-elapsed">{view.elapsed}</span>}
            </div>
            <Composer
              key={`steer-${jobId}`}
              sessionId={session.id}
              session={session}
              shortPlaceholder
              steer={{
                jobId,
                name: view.name,
                onRebound: onBack,
                onStop: onCancel,
                stopArmed: confirmCancel,
              }}
            />
          </>
        )}
        <BranchStatusLine session={session} view={view} />
      </div>
    </div>
  );
}

// BranchStatusLine — the parent's status line, measuring the branch. Every
// number here belongs to the CHILD (its own context window, its own spend, its
// own model and effort, its own tokens); the permission mode is the session's,
// and stays because it is the policy the child's tools run under. Nothing is a
// door: a child's settings aren't changed from inside it, so each segment is
// the same face without the tap.
function BranchStatusLine({ session, view }) {
  const usage = view.usage;
  return (
    <StatusLineRow
      contextPercent={view.contextPercent}
      contextLabel={`Subagent context ${view.contextPercent}% used`}
      cost={usage && usage.costUSD > 0 ? fmtCost(usage.costUSD) : undefined}
      model={view.model}
      modelAccent={modelAccent(view.model)}
      thinking={view.thinking}
      perm={session.permissionMode || "yolo"}
      tokensUp={(usage && usage.inputTokens) || 0}
      tokensDown={(usage && usage.outputTokens) || 0}
    />
  );
}

function MobileOutcome({ view, onBack }) {
  const [copied, setCopied] = useState(false);
  const usage = view.usage;
  const segs = [];
  if (view.elapsed) segs.push(view.elapsed);
  if (usage && usage.costUSD > 0) segs.push(`$${usage.costUSD.toFixed(3)}`);
  if (usage && (usage.inputTokens || usage.outputTokens)) {
    segs.push(`↑${fmtTokens(usage.inputTokens || 0)} ↓${fmtTokens(usage.outputTokens || 0)}`);
  }
  const meta = segs.join(" · ");

  const copy = (text) => {
    if (!text) return;
    copyToClipboard(text).then((ok) => {
      if (!ok) return;
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    });
  };

  return (
    <div class={`msa-outcome ${view.outcome}`}>
      <div class="msa-outcome-head">
        {view.outcome === "completed" && (
          <span class="msa-outcome-check"><Check size={13} strokeWidth={2.5} /></span>
        )}
        {view.outcome === "failed" && <X size={15} aria-hidden="true" />}
        <b>{view.outcome}</b>
        {meta && <span class="msa-outcome-meta">· {meta}</span>}
      </div>
      {view.outcome === "failed" && view.error && (
        <div class="msa-outcome-err">{String(view.error).split("\n").slice(0, 4).join("\n")}</div>
      )}
      {view.outcome === "completed" && view.resultChip && (
        <div class="msa-outcome-chip">{view.resultChip}</div>
      )}
      <div class="msa-outcome-actions">
        {view.outcome === "failed" && (
          <button type="button" class="msa-outcome-btn" onClick={() => copy(view.error || "")}>
            {copied ? "copied ✓" : <>{<Copy size={13} />} Copy error</>}
          </button>
        )}
        {view.outcome === "completed" && (
          <button type="button" class="msa-outcome-btn" onClick={() => copy(view.result || "")}>
            {copied ? "copied ✓" : <>{<Copy size={13} />} Copy result</>}
          </button>
        )}
        <button type="button" class="msa-outcome-back" onClick={onBack}>Back to parent</button>
      </div>
    </div>
  );
}
