import { useEffect, useState } from "preact/hooks";
import { ArrowLeft, GitFork, Rocket, X, Check, Copy } from "lucide-preact";
import { Spinner, Kbd, IconButton } from "../../primitives/index.js";
import { ModelPill, TokenFlow, UserWaypoint } from "../../components/index.js";
import { Stream } from "../Stream/Stream.jsx";
import { Composer } from "../Composer/Composer.jsx";
import { subagentView, canPromote } from "../../data/subagent-view-model.js";
import { fmtTokens, copyToClipboard, sessionTitle } from "../../data/util/format.js";
import { modelAccent } from "../../data/selectors.js";
import { cancelSubagent, promoteSubagent } from "../../data/session-actions.js";
import { updateSession } from "../../data/store.js";
import "./SubagentView.css";

// SubagentView — "inside the fork" (5J). Zoom into ONE subagent: its
// transcript rendered by the SAME Stream as the parent (cero divergencia,
// INC-37), framed by a breadcrumb header, a sibling rail, a task card, a fused
// now-line, and — on terminal — an outcome banner.
//
// It reuses the pure projection subagentView(session, jobId) for everything
// data-shaped (accent, siblings, blocks, terminal outcome); this component only
// paints the frame and wires the actions. When the viewed subagent no longer
// exists (pruned), the projection returns null → we "rebound" to the parent
// (clear viewingSubagent) rather than show an empty view.
//
// Props: { session, jobId, onBack }. onBack clears viewingSubagent.

export function SubagentView({ session, jobId, onBack }) {
  const view = subagentView(session, jobId);

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

  // Keyboard: Esc → back; [ / ] → cycle siblings. Skipped while typing in the
  // steer composer (or any input/textarea/contenteditable) so `[`/`]` can be
  // typed normally instead of being swallowed by the sibling cycler.
  useEffect(() => {
    if (!view) return;
    const onKey = (e) => {
      const t = e.target;
      const typing = t && (t.tagName === 'TEXTAREA' || t.tagName === 'INPUT' || t.isContentEditable);
      if (e.key === "Escape") { e.preventDefault(); onBack?.(); return; }
      if (typing) return;
      if ((e.key === "[" || e.key === "]") && view.siblings.length > 1) {
        e.preventDefault();
        const idx = view.siblings.findIndex((s) => s.active);
        if (idx < 0) return;
        const n = view.siblings.length;
        const next = e.key === "]" ? (idx + 1) % n : (idx - 1 + n) % n;
        onSibling(view.siblings[next].id);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [view, onBack]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!view) return null;

  const accent = view.accent;
  const accentVar = `var(--${accent})`;

  const onCancel = () => {
    if (!confirmCancel) { setConfirmCancel(true); return; }
    setConfirmCancel(false);
    cancelSubagent(session.id, jobId).catch(() => {});
  };
  const onPromote = () => { promoteSubagent(session.id, jobId).catch(() => {}); };
  const onSibling = (id) => { updateSession(session.id, { viewingSubagent: id }); };

  return (
    <div
      class="subagent-view"
      role="region"
      aria-label={`Subagent ${view.name}, ${view.status}`}
    >
      <header class="sa-head">
        <IconButton label="Back to parent" onClick={onBack}>
          <ArrowLeft size={15} />
        </IconButton>
        <div class="sa-crumb">
          <GitFork size={13} style={{ color: accentVar }} aria-hidden="true" />
          <button type="button" class="sa-crumb-parent" onClick={onBack}>
            {sessionTitle(session)}
          </button>
          <span class="sa-crumb-sep" aria-hidden="true">›</span>
          <span class="sa-crumb-name" style={{ color: accentVar }}>{view.name}</span>
        </div>
        <div class="sa-head-actions">
          {view.model && (
            <ModelPill model={view.model} level={view.thinking} accent={modelAccent(view.model)} variant="glyph" />
          )}
          {canPromote(view) && (
            <IconButton label="Promote — run in background, unblocks parent" onClick={onPromote}>
              <Rocket size={15} />
            </IconButton>
          )}
          {!view.terminal && (
            <button
              type="button"
              class={`sa-cancel${confirmCancel ? " armed" : ""}`}
              onClick={onCancel}
              title="Cancel subagent"
              aria-label="Cancel subagent"
            >
              {confirmCancel ? "sure?" : <X size={15} />}
            </button>
          )}
          <Kbd>esc</Kbd>
        </div>
      </header>

      {view.siblings.length > 1 && (
        <div class="sa-rail" role="tablist" aria-label="Sibling subagents">
          {view.siblings.map((s) => (
            <button
              type="button"
              key={s.id}
              role="tab"
              aria-selected={s.active}
              class={`sa-rail-chip${s.active ? " active" : ""}`}
              style={s.active ? { borderColor: `var(--${s.accent})` } : undefined}
              onClick={() => onSibling(s.id)}
            >
              <Spinner color={s.accent} size={9} />
              <span class="sa-rail-name" style={{ color: `var(--${s.accent})` }}>{s.name}</span>
            </button>
          ))}
          <Kbd>[ ]</Kbd>
        </div>
      )}

      <div class="sa-body">
        <Stream
          session={{ id: `${session.id}:${jobId}`, messages: [] }}
          blocks={view.blocks}
          lead={
            <UserWaypoint className="sa-task" time={undefined}>
              <div class="sa-task-label" style={{ color: accentVar }}>TASK — from parent</div>
              <p>{view.task || "(no task recorded)"}</p>
            </UserWaypoint>
          }
        />
      </div>

      {view.terminal ? (
        <OutcomeBanner view={view} onBack={onBack} />
      ) : (
        <>
          <Composer
            key={`steer-${jobId}`}
            sessionId={session.id}
            session={session}
            steer={{
              jobId,
              name: view.name,
              onRebound: onBack,
            }}
          />
          <NowLine view={view} />
        </>
      )}
    </div>
  );
}

// NowLine — fused status below the composer. Segments missing a backend datum
// are omitted rather than shown as undefined/NaN.
function NowLine({ view }) {
  const usage = view.usage;
  const hasTokens = usage && (usage.inputTokens != null || usage.outputTokens != null);
  const cost = usage && usage.costUSD > 0 ? `~$${usage.costUSD.toFixed(3)}` : null;
  return (
    <div class="sa-nowline" aria-hidden="true">
      <span class="sa-nowline-act is-live">
        {view.action || "working"}{view.elapsed ? ` · ${view.elapsed}` : ""}
      </span>
      {hasTokens && (
        <span class="sa-nowline-tok">
          <TokenFlow up={usage.inputTokens || 0} down={usage.outputTokens || 0} variant="strip" />
        </span>
      )}
      {cost && <span class="sa-nowline-cost">{cost}</span>}
    </div>
  );
}

// OutcomeBanner — terminal state (INC-38): green completed / red failed /
// neutral cancelled. Enters once (fade+rise) then stays still.
function OutcomeBanner({ view, onBack }) {
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

  if (view.outcome === "failed") {
    const err = view.error || "The subagent failed.";
    return (
      <div class="sa-outcome failed">
        <div class="sa-outcome-head">
          <X size={15} aria-hidden="true" />
          <b>failed</b>
          {meta && <span class="sa-outcome-meta">· {meta}</span>}
        </div>
        {view.error && <div class="sa-outcome-err">{firstLines(view.error)}</div>}
        <div class="sa-outcome-actions">
          <button type="button" class="sa-outcome-btn" onClick={() => copy(err)}>
            {copied ? "copied ✓" : <>{<Copy size={13} />} Copy error</>}
          </button>
          <button type="button" class="sa-outcome-btn primary" onClick={onBack}>Back to parent</button>
        </div>
      </div>
    );
  }

  if (view.outcome === "cancelled") {
    return (
      <div class="sa-outcome cancelled">
        <div class="sa-outcome-head">
          <b>cancelled</b>
          {meta && <span class="sa-outcome-meta">· {meta}</span>}
        </div>
        <div class="sa-outcome-actions">
          <button type="button" class="sa-outcome-btn primary" onClick={onBack}>Back to parent</button>
        </div>
      </div>
    );
  }

  // completed
  return (
    <div class="sa-outcome completed">
      <div class="sa-outcome-head">
        <span class="sa-outcome-check" aria-hidden="true"><Check size={12} strokeWidth={2.5} /></span>
        <b>completed</b>
        {meta && <span class="sa-outcome-meta">· {meta}</span>}
        {view.resultChip && <span class="sa-outcome-chip">{view.resultChip}</span>}
      </div>
      <div class="sa-outcome-actions">
        <button type="button" class="sa-outcome-btn" onClick={() => copy(view.resultChip || "")}>
          {copied ? "copied ✓" : <>{<Copy size={13} />} Copy result</>}
        </button>
        <button type="button" class="sa-outcome-btn primary" onClick={onBack}>Back to parent</button>
      </div>
    </div>
  );
}

// firstLines trims a long error to a few readable lines for the banner body.
function firstLines(str, n = 4) {
  const lines = String(str).split("\n").slice(0, n);
  return lines.join("\n");
}
