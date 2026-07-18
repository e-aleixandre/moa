import { GitFork, Check } from "lucide-preact";
import "./FanoutBlock.css";

// FanoutBlock — parallel-subagent block inside the conversation
// stream (fan-out): lives as a *content* piece between paragraphs of
// AssistantDocument, just like ActivityLedger/DiffBlock — hence why it's in
// components/ and not layout/ (layout/ is for screen organisms like
// Spine/ChatHead/PaneGrid, not for blocks that appear inside the stream).
//
// Each agent row reuses Spinner (same tokenized pattern as
// AgentTray) for the "running" state, and a green check for "done". The
// accent color (sky/teal/mauve/...) visually distinguishes each subagent
// in the spinner, the indeterminate bar and the name alike.
import { Spinner } from "../../primitives/index.js";

function RunningRow({ name, accent = "sky", action, time }) {
  return (
    <div class="agent-row">
      <div class="agent-id">
        <Spinner color={accent} />
        <span class="nm" style={{ color: `var(--${accent})` }}>{name}</span>
      </div>
      <div class="agent-live">
        <span class="act">
          ▸ <span class="cur">{action}</span>
        </span>
        <div class={`ibar c-${accent}`} aria-hidden="true">
          <i />
        </div>
      </div>
      <div class="agent-time">{time}</div>
    </div>
  );
}

function DoneRow({ name, result, resultDesc, onViewReport }) {
  return (
    <div class="agent-row done">
      <div class="agent-id">
        <span class="check" aria-hidden="true">
          <Check size={9} strokeWidth={2.5} />
        </span>
        <span class="nm">{name}</span>
      </div>
      <div class="result">
        {result && <span class="fanout-result-chip">{result}</span>}
        {resultDesc && <span class="desc">{resultDesc}</span>}
      </div>
      {onViewReport && (
        <button type="button" class="view" onClick={onViewReport}>
          view report →
        </button>
      )}
    </div>
  );
}

// FanoutBlock — props.agents: array of
// { id, name, accent, state: "running"|"done", action, time, result, resultDesc, onViewReport }
// `id` is each subagent's stable key (recommended for live states
// that update/reorder); falls back to the name if missing.
export function FanoutBlock({ task, count, startedAt, agents = [], onViewReport }) {
  const n = count ?? agents.length;
  return (
    <div class="fanout">
      <div class="fanout-head">
        <GitFork size={14} aria-hidden="true" />
        <b>{n} subagents</b>
        {task && <span> · {task}</span>}
        {startedAt && <span class="n">started {startedAt}</span>}
      </div>

      {agents.map((a) =>
        a.state === "done" ? (
          <DoneRow key={a.id ?? a.name} {...a} onViewReport={a.onViewReport || onViewReport} />
        ) : (
          <RunningRow key={a.id ?? a.name} {...a} />
        )
      )}
    </div>
  );
}
