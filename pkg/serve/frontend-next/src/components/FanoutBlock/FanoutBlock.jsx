import { GitFork, Check } from "lucide-preact";
import "./FanoutBlock.css";

// FanoutBlock — bloque de subagentes en paralelo dentro del stream de
// conversación (fan-out): vive como pieza de *contenido* entre párrafos de
// AssistantDocument, igual que ActivityLedger/DiffBlock — por eso está en
// components/ y no en layout/ (layout/ es para organismos de pantalla como
// Spine/ChatHead/PaneGrid, no para bloques que aparecen dentro del stream).
//
// Cada fila de agente reutiliza Spinner (mismo patrón tokenizado que
// AgentTray) para el estado "running", y un check verde para "done". El
// color de acento (sky/teal/mauve/...) distingue visualmente cada subagente
// tanto en el spinner como en la barra indeterminada y el nombre.
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

// FanoutBlock — props.agents: array de
// { id, name, accent, state: "running"|"done", action, time, result, resultDesc, onViewReport }
// `id` es la clave estable de cada subagente (recomendado para estados vivos
// que se actualizan/reordenan); si falta, cae al nombre.
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
