import { useEffect, useState } from "preact/hooks";
import { ChevronLeft, Rocket, X, Check, Copy } from "lucide-preact";
import { Spinner } from "../../../primitives/index.js";
import { UserWaypoint } from "../../../components/index.js";
import { Composer } from "../../Composer/Composer.jsx";
import { MobileStream } from "./MobileStream.jsx";
import { subagentView, canPromote } from "../../../data/subagent-view-model.js";
import { fmtTokens, copyToClipboard } from "../../../data/util/format.js";
import { cancelSubagent, promoteSubagent } from "../../../data/session-actions.js";
import { updateSession } from "../../../data/store.js";
import "./MobileSubagentView.css";

// MobileSubagentView — full-screen push counterpart of the desktop SubagentView
// (5J, INC-34). Same anatomy, stacked for a phone: header with ‹ back + codename
// + state, Promote/Cancel as 44px ICON buttons (cancel with inline "sure?"),
// sibling rail as a horizontal scroll strip, task card, MobileStream body, and a
// steer composer (textarea ≥16px anti-zoom, inherited from the shared Composer).
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
  const onSibling = (id) => { updateSession(session.id, { viewingSubagent: id }); };

  const usage = view.usage;
  const tokens = usage && (usage.inputTokens || usage.outputTokens)
    ? `↑${fmtTokens(usage.inputTokens || 0)} ↓${fmtTokens(usage.outputTokens || 0)}`
    : null;
  const cost = usage && usage.costUSD > 0 ? `$${usage.costUSD.toFixed(3)}` : null;

  const threadClass = view.terminal ? `thread-${view.outcome}` : "";

  return (
    <div class={`msa ${threadClass}`} style={{ "--sa-accent": `var(--${accent})` }}>
      <header class="msa-head">
        <button type="button" class="msa-back" aria-label="Back to parent" onClick={onBack}>
          <ChevronLeft size={20} />
        </button>
        <div class="msa-crumb">
          <span class="msa-name" style={{ color: `var(--${accent})` }}>{view.name}</span>
          <span class="msa-state">
            {!view.terminal && <Spinner color={accent} size={8} />}
            {view.terminal ? view.outcome : "working"}
          </span>
        </div>
        <div class="msa-actions">
          {canPromote(view) && (
            <button type="button" class="msa-abtn" aria-label="Promote to background" onClick={onPromote}>
              <Rocket size={18} />
            </button>
          )}
          {!view.terminal && (
            <button
              type="button"
              class={`msa-abtn cancel${confirmCancel ? " armed" : ""}`}
              aria-label="Cancel subagent"
              onClick={onCancel}
            >
              {confirmCancel ? <span class="msa-sure">sure?</span> : <X size={18} />}
            </button>
          )}
        </div>
      </header>

      {view.siblings.length > 1 && (
        <div class="msa-rail">
          {view.siblings.map((s) => (
            <button
              type="button"
              key={s.id}
              class={`msa-rail-chip${s.active ? " active" : ""}`}
              style={s.active ? { borderColor: `var(--${s.accent})` } : undefined}
              onClick={() => onSibling(s.id)}
            >
              <Spinner color={s.accent} size={8} />
              <span style={{ color: `var(--${s.accent})` }}>{s.name}</span>
            </button>
          ))}
        </div>
      )}

      <div class="msa-body">
        <UserWaypoint className="msa-task" time={undefined}>
          <div class="msa-task-label" style={{ color: `var(--${accent})` }}>TASK — from parent</div>
          <p>{view.task || "(no task recorded)"}</p>
        </UserWaypoint>
        <MobileStream session={{ id: `${session.id}:${jobId}`, messages: [] }} blocks={view.blocks} />
      </div>

      {view.terminal ? (
        <MobileOutcome view={view} onBack={onBack} />
      ) : (
        <div class="msa-foot">
          <div class="msa-nowline">
            <Spinner color={accent} size={9} />
            {view.action && <span class="msa-now-act">{view.action}</span>}
            {view.elapsed && <span class="msa-now-seg">{view.elapsed}</span>}
            {tokens && <span class="msa-now-seg">{tokens}</span>}
            {cost && <span class="msa-now-seg">{cost}</span>}
          </div>
          <Composer
            key={`steer-${jobId}`}
            sessionId={session.id}
            session={session}
            shortPlaceholder
            steer={{ jobId, accent, name: view.name, onRebound: onBack }}
          />
        </div>
      )}
    </div>
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
          <button type="button" class="msa-outcome-btn" onClick={() => copy(view.resultChip || "")}>
            {copied ? "copied ✓" : <>{<Copy size={13} />} Copy result</>}
          </button>
        )}
        <button type="button" class="msa-outcome-back" onClick={onBack}>Back to parent</button>
      </div>
    </div>
  );
}
