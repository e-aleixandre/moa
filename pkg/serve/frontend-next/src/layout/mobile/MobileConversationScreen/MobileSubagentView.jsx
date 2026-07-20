import { useEffect, useState } from "preact/hooks";
import { ChevronLeft, GitFork, Rocket, X, Check, Copy } from "lucide-preact";
import { Spinner } from "../../../primitives/index.js";
import { ModelPill, UserWaypoint } from "../../../components/index.js";
import { Composer } from "../../Composer/Composer.jsx";
import { MobileStream } from "./MobileStream.jsx";
import { subagentView, canPromote } from "../../../data/subagent-view-model.js";
import { fmtTokens, copyToClipboard, truncateText, sessionTitle } from "../../../data/util/format.js";
import { modelAccent } from "../../../data/selectors.js";
import { cancelSubagent, promoteSubagent } from "../../../data/session-actions.js";
import { updateSession } from "../../../data/store.js";
import "./MobileSubagentView.css";

// MobileSubagentView — full-screen push counterpart of the desktop SubagentView
// (5J, INC-34). It wears the PARENT thread's chrome (SUBAGENT-VIEW-RECONCILE):
// the header is a variant of the shared `.mhead` and the composer sits in the
// `.mcomposer` pill, so the fork reads as "the same house, zoomed into a
// branch" rather than a foreign screen. What stays fork-proper: the full-screen
// push, the 2px accent thread, the breadcrumb/codename in accent, the sibling
// rail, the task card, the below-composer now-line, and the terminal outcome
// banner. Reuses the pure subagentView() projection; rebounds to the parent
// when the subagent was pruned.

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
      <header class="mhead msa-head">
        <div class="mhead-row">
          <button type="button" class="msa-back" aria-label="Back to parent" onClick={onBack}>
            <ChevronLeft size={20} />
          </button>
          <div class="mhead-switch msa-ident" role="presentation">
            <GitFork size={13} style={{ color: `var(--${accent})` }} aria-hidden="true" />
            <span class="mhead-title" style={{ color: `var(--${accent})` }}>{view.name}</span>
            {!view.terminal && <Spinner color={accent} size={8} />}
          </div>
          {view.model && (
            <ModelPill model={view.model} level={view.thinking} accent={modelAccent(view.model)} variant="glyph" />
          )}
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
        </div>
        <div class="mhead-sub">
          <span class="mhead-meta msa-crumbline">
            <button type="button" class="msa-parent" onClick={onBack}>
              {sessionTitle(session)}
            </button>
            {view.task ? ` › ${truncateText(view.task, 60)}` : ""}
          </span>
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

      <MobileStream
        session={{ id: `${session.id}:${jobId}`, messages: [] }}
        blocks={view.blocks}
        lead={
          <UserWaypoint className="msa-task" time={undefined}>
            <div class="msa-task-label" style={{ color: `var(--${accent})` }}>TASK — from parent</div>
            <p>{view.task || "(no task recorded)"}</p>
          </UserWaypoint>
        }
      />

      {view.terminal ? (
        <MobileOutcome view={view} onBack={onBack} />
      ) : (
        <div class="mcomposer msa-foot">
          <Composer
            key={`steer-${jobId}`}
            sessionId={session.id}
            session={session}
            shortPlaceholder
            steer={{ jobId, name: view.name, onRebound: onBack }}
          />
          {/* Now-line sits BELOW the composer, in the parent's status slot
              (SUBAGENT-VIEW-RECONCILE §2.3): telemetry lives on the mono line
              under the input everywhere in the app. Accent colors the activity. */}
          <div class="mcomposer-status msa-nowline">
            <span class="work msa-now-act" style={{ color: `var(--${accent})` }}>
              <Spinner color={accent} size={8} /> {view.action || "working"}
              {view.elapsed ? ` · ${view.elapsed}` : ""}
            </span>
            {tokens && <span class="tokens">{tokens}</span>}
            {cost && <span class="spend">{cost}</span>}
          </div>
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
