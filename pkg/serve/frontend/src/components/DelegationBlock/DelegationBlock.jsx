import { useState, useEffect, useRef } from "preact/hooks";
import { GitFork, Check, X, ChevronDown, ChevronRight } from "lucide-preact";
import "./DelegationBlock.css";
import { Spinner } from "../../primitives/index.js";
import { copyToClipboard } from "../../data/util/format.js";

// DelegationBlock — replaces FanoutBlock as the ONE surface for a wave of
// subagents in the stream (SUBAGENTS-REDESIGN-SPEC-FABLE.md §1). It renders
// the { type:'delegation' } block emitted by projectStream: a header
// (skipped for a lone agent — spec §1.3.2), one row per agent that mutates
// in place (running → done/failed/cancelled, spec §1.3.3), and a live
// hairline while the wave is unsettled. When settled it starts collapsed to
// the header line (spec §1.3.4); a tap re-expands it.
//
// Running rows reuse Spinner + the indeterminate `.ibar` from the old
// FanoutBlock (task instruction) rather than the plain breathing dot drawn
// in the visual mockup — same tokenized "live" language already used
// elsewhere (AgentTray, tool tickers), so it's kept instead of introducing a
// third "alive" idiom.
//
// bashJobs (nested `└ $` rows under their owning agent, spec §2) always
// arrive empty in this phase — the row is left prepared but unrendered.

function summaryText(summary) {
  const { done, failed } = summary;
  if (!done && !failed) return "";
  if (done && failed) return `· ${done} ✓ · ${failed} ✗`;
  if (failed) return `· ${failed} ✗`;
  return `· ${done} done`;
}

function NestedBashRow({ job, accent }) {
  // Interim: bashJobs is always [] for now (spec §2 nesting needs backend
  // parent_job_id support). Row kept minimal so it doesn't break once
  // populated.
  return (
    <div class="dlg-nest" style={{ "--a": `var(--${accent})` }}>
      <span class="n-sig">└ $</span>
      <span class="n-cmd">{job.cmd}</span>
      {job.progress && <span class="n-prog">{job.progress}</span>}
      {job.elapsed && <span class="n-time">{job.elapsed}</span>}
    </div>
  );
}

function RunningAgentRow({ agent, onOpenAgent }) {
  const { id, name, accent = "sky", action, time, bashJobs = [] } = agent;
  const clickable = !!onOpenAgent;
  const Tag = clickable ? "button" : "div";
  return (
    <>
      <Tag
        class={`dlg-agent${clickable ? " clickable" : ""}`}
        style={{ "--a": `var(--${accent})` }}
        onClick={clickable ? () => onOpenAgent(id) : undefined}
        type={clickable ? "button" : undefined}
        data-live-id={id}
      >
        <span class="a-id">
          <Spinner color={accent} size={11} />
          <span class="a-name">{name}</span>
        </span>
        {action ? (
          <span class="a-act">
            ▸ <span class="cur">{action}</span>
          </span>
        ) : (
          <span class="a-act" />
        )}
        {time && <span class="a-time">{time}</span>}
      </Tag>
      <div class={`dlg-ibar c-${accent}`} aria-hidden="true">
        <i />
      </div>
      {bashJobs.map((job) => (
        <NestedBashRow key={job.id || job.cmd} job={job} accent={accent} />
      ))}
    </>
  );
}

function DoneAgentRow({ agent, onOpenAgent }) {
  const { id, name, accent = "sky", state, result, time } = agent;
  const failed = state === "failed" || state === "cancelled";
  const [expanded, setExpanded] = useState(false);
  const [copied, setCopied] = useState(false);
  const canExpand = !!result;
  const RowTag = canExpand ? "button" : "div";
  return (
    <div class="dlg-complete" style={{ "--a": `var(--${accent})` }}>
      <RowTag
        {...(canExpand ? { type: "button" } : {})}
        class={`dlg-agent ${failed ? "failed" : "done"}${canExpand ? " clickable" : ""}`}
        onClick={canExpand ? () => setExpanded((open) => !open) : undefined}
        aria-expanded={canExpand ? expanded : undefined}
      >
        <span class="a-id">
          {failed ? (
            <span class="fail-x" aria-hidden="true">
              {state === "cancelled" ? "⊘" : <X size={12} strokeWidth={2.5} />}
            </span>
          ) : (
            <span class="a-dot" aria-hidden="true">
              <Check size={12} strokeWidth={2.5} />
            </span>
          )}
          <span class="a-name">{name}</span>
        </span>
        <span class={`a-state${failed ? " failed" : ""}`}>{state === "cancelled" ? "Cancelled" : failed ? "Failed" : "Completed"}</span>
        {time && <span class="a-time">{time}</span>}
        {canExpand && <ChevronDown class={`a-expand${expanded ? " open" : ""}`} size={13} aria-hidden="true" />}
      </RowTag>
      {onOpenAgent && (
        <button type="button" class="dlg-open" onClick={() => onOpenAgent(id)} aria-label={`Open ${name} conversation`}>
          <span>conversation</span><ChevronRight size={14} aria-hidden="true" />
        </button>
      )}
      {expanded && <div class="dlg-result-body">
        <span class="dlg-result-label">Result</span>
        <pre>{result}</pre>
        <button type="button" class="dlg-copy" onClick={() => copyToClipboard(result).then((ok) => { if (ok) { setCopied(true); setTimeout(() => setCopied(false), 1200); } })}>{copied ? "Copied ✓" : "Copy result"}</button>
      </div>}
    </div>
  );
}

function OutcomeAgentRow({ agent, onOpenAgent }) {
  const { id, name, accent = "sky", state, action, chip, result, time } = agent;
  const terminal = state !== "running";
  const failed = state === "failed";
  const cancelled = state === "cancelled";
  const [expanded, setExpanded] = useState(false);
  const [copied, setCopied] = useState(false);
  const copiedTimer = useRef(null);
  useEffect(() => () => clearTimeout(copiedTimer.current), []);
  const canExpand = !!result;
  const SummaryTag = canExpand ? "button" : "div";
  const preview = terminal ? (chip || (cancelled ? "Cancelled before producing a result." : "No result returned.")) : action || "Working";

  return (
    <div class={`dlg-outcome-card${failed ? " failed" : ""}${cancelled ? " cancelled" : ""}`} style={{ "--a": `var(--${accent})` }}>
      <SummaryTag
        {...(canExpand ? { type: "button" } : {})}
        class={`dlg-outcome-summary${canExpand ? " clickable" : ""}`}
        onClick={canExpand ? () => setExpanded((open) => !open) : undefined}
        aria-expanded={canExpand ? expanded : undefined}
        aria-label={canExpand ? `${state === "done" ? "Completed" : failed ? "Failed" : "Cancelled"}: ${name}` : undefined}
      >
        <span class="outcome-title">
          <span class="outcome-mark" aria-hidden="true">{state === "running" ? <Spinner color={accent} size={11} /> : cancelled ? "⊘" : failed ? <X size={12} strokeWidth={2.5} /> : <Check size={12} strokeWidth={2.5} />}</span>
          <span class="outcome-name">{name}</span>
          {time && <span class="outcome-time">{time}</span>}
        </span>
        <span class="outcome-preview">{state === "running" && "▸ "}{preview}</span>
      </SummaryTag>
      {(canExpand || onOpenAgent) && <div class={`dlg-outcome-actions${canExpand && onOpenAgent ? "" : " single"}`}>
        {canExpand && <button type="button" onClick={() => setExpanded((open) => !open)} aria-expanded={expanded}>Result <ChevronDown class={expanded ? "open" : ""} size={13} aria-hidden="true" /></button>}
        {onOpenAgent && <button type="button" onClick={() => onOpenAgent(id)} aria-label={`Open ${name} conversation`}>Conversation <ChevronRight size={14} aria-hidden="true" /></button>}
      </div>}
      {expanded && <div class="dlg-result-body">
        <span class="dlg-result-label">Result</span><pre>{result}</pre>
        <button type="button" class="dlg-copy" onClick={() => copyToClipboard(result).then((ok) => { if (ok) { clearTimeout(copiedTimer.current); setCopied(true); copiedTimer.current = setTimeout(() => setCopied(false), 1200); } })}>{copied ? "Copied ✓" : "Copy result"}</button>
      </div>}
    </div>
  );
}

// DelegationBlock — props.agents: array of
// { id, name, accent, state:'running'|'done'|'failed'|'cancelled', action?,
// time?, chip?, result?, bashJobs:[] } as emitted by stream-model.js. `summary` is
// { total, done, failed }; `settled` is true once no agent is running.
// `onOpenAgent(id)` opens the subagent's detail view on row click.
export function DelegationBlock({ agents = [], summary, settled, onOpenAgent, variant = "outcome" }) {
  const total = summary?.total ?? agents.length;
  const showHeader = total > 1;
  // Settled blocks start collapsed to the header line (spec §1.3.4); live
  // ones start expanded. A lone agent (no header) is never collapsible.
  const [collapsed, setCollapsed] = useState(showHeader && !!settled);
  // Auto-collapse when a block that was live finishes (settled flips
  // false→true): the block outlives its data by id, so useState alone won't
  // react. Collapse once on the transition; the user can still re-expand.
  const wasSettledRef = useRef(!!settled);
  useEffect(() => {
    if (settled && !wasSettledRef.current && showHeader) setCollapsed(true);
    wasSettledRef.current = !!settled;
  }, [settled, showHeader]);

  return (
    <div
      class={`dlg${settled ? " settled" : ""}${collapsed ? " collapsed" : ""}`}
      {...(settled ? {} : { "data-live-surface": "delegation" })}
    >
      {showHeader && (
        <button
          type="button"
          class="dlg-head"
          onClick={() => setCollapsed((c) => !c)}
          aria-expanded={!collapsed}
        >
          <span class="fork" aria-hidden="true">
            <GitFork size={14} />
          </span>
          <b>{total} agents</b>
          <span class="sum">{summaryText(summary || {})}</span>
          <span class="chev" aria-hidden="true">
            <ChevronDown size={12} />
          </span>
        </button>
      )}

      {!collapsed &&
        agents.map((a) =>
          variant === "outcome" ? (
            <OutcomeAgentRow key={a.id ?? a.name} agent={a} onOpenAgent={onOpenAgent} />
          ) : a.state === "running" ? (
            <RunningAgentRow key={a.id ?? a.name} agent={a} onOpenAgent={onOpenAgent} />
          ) : (
            <DoneAgentRow key={a.id ?? a.name} agent={a} onOpenAgent={onOpenAgent} />
          )
        )}

      {!collapsed && !settled && <div class="dlg-life" aria-hidden="true" />}
    </div>
  );
}
