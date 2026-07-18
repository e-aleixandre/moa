import "./AgentTray.css";

// AgentTray — fila de chips de subagentes/herramientas vivas, encima del
// composer. Cada chip: spinner + nombre coloreado + qué hace + tiempo mono.
const AGENTS = [
  { key: "terra", who: "terra", color: "sky", what: "reviewing diff · pkg/serve", time: "2m 14s" },
  { key: "bash-race", who: "bash", color: "teal", what: "go test -race ./pkg/bus", time: "0m 41s" },
];

export function AgentTray({ agents = AGENTS }) {
  if (!agents.length) return null;
  return (
    <div class="agent-tray">
      {agents.map((a) => (
        <button type="button" key={a.key} class="agent-chip">
          <span class="agent-spinner" aria-hidden="true" />
          <span class="agent-who" style={{ color: `var(--${a.color})` }}>{a.who}</span>
          <span class="agent-what">{a.what}</span>
          <span class="agent-time">{a.time}</span>
        </button>
      ))}
    </div>
  );
}
