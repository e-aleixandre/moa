import { Check } from "lucide-preact";
import "./AgentTray.css";
import { Spinner } from "../../primitives/index.js";

// AgentTray — fila de chips de subagentes/herramientas vivas, encima del
// composer. Cada chip: spinner + nombre coloreado + qué hace + tiempo mono.
// El spinner reutiliza la primitiva Spinner (mismo patrón que antes vivía
// duplicado aquí como .agent-spinner, ahora tokenizado con variantes de color
// para poder distinguir varios agentes en paralelo — ver FanoutBlock).
//
// state: "running" (default, spinner) | "done" (check verde + badge peach
// "unseen" pulsante, Fase 3B — mirrors the finished-but-unread row of
// FanoutBlock when you've scrolled away from the stream).
const AGENTS = [
  { key: "terra", who: "terra", color: "sky", what: "reviewing diff · pkg/serve", time: "2m 14s" },
  { key: "bash-race", who: "bash", color: "teal", what: "go test -race ./pkg/bus", time: "0m 41s" },
];

export function AgentTray({ agents = AGENTS }) {
  if (!agents.length) return null;
  return (
    <div class="agent-tray">
      {agents.map((a) => (
        <button
          type="button"
          key={a.key}
          class={`agent-chip${a.state === "done" ? " done" : ""}`}
        >
          {a.state === "done" ? (
            <span class="agent-check" aria-hidden="true">
              <Check size={12} strokeWidth={2.5} />
            </span>
          ) : (
            <Spinner color={a.color || "blue"} />
          )}
          <span class="agent-who" style={a.state === "done" ? undefined : { color: `var(--${a.color || "blue"})` }}>
            {a.who}
          </span>
          <span class="agent-what">{a.what}</span>
          {a.state === "done" ? (
            <span class="agent-badge" aria-hidden="true" />
          ) : (
            <span class="agent-time">{a.time}</span>
          )}
        </button>
      ))}
    </div>
  );
}
