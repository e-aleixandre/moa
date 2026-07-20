import { useState, useEffect, useRef } from "preact/hooks";
import { GitFork, Check, X, ChevronDown } from "lucide-preact";
import "./DelegationBlock.css";
import { Spinner } from "../../primitives/index.js";

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
  const { id, name, accent = "sky", state, chip, time } = agent;
  const failed = state === "failed" || state === "cancelled";
  const clickable = !!onOpenAgent;
  const Tag = clickable ? "button" : "div";
  return (
    <Tag
      class={`dlg-agent ${failed ? "failed" : "done"}${clickable ? " clickable" : ""}`}
      style={{ "--a": `var(--${accent})` }}
      onClick={clickable ? () => onOpenAgent(id) : undefined}
      type={clickable ? "button" : undefined}
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
      <span class="a-result">
        {chip && <span class={`a-chip${failed ? " err" : ""}`}>{chip}</span>}
      </span>
      {time && <span class="a-time">{time}</span>}
    </Tag>
  );
}

// DelegationBlock — props.agents: array of
// { id, name, accent, state:'running'|'done'|'failed'|'cancelled', action?,
// time?, chip?, bashJobs:[] } as emitted by stream-model.js. `summary` is
// { total, done, failed }; `settled` is true once no agent is running.
// `onOpenAgent(id)` opens the subagent's detail view on row click.
export function DelegationBlock({ agents = [], summary, settled, onOpenAgent }) {
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
    <div class={`dlg${settled ? " settled" : ""}${collapsed ? " collapsed" : ""}`}>
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
          a.state === "running" ? (
            <RunningAgentRow key={a.id ?? a.name} agent={a} onOpenAgent={onOpenAgent} />
          ) : (
            <DoneAgentRow key={a.id ?? a.name} agent={a} onOpenAgent={onOpenAgent} />
          )
        )}

      {!collapsed && !settled && <div class="dlg-life" aria-hidden="true" />}
    </div>
  );
}
