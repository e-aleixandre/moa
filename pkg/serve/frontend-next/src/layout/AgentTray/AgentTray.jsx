import { Check } from "lucide-preact";
import "./AgentTray.css";
import { Spinner } from "../../primitives/index.js";

// AgentTray — sticky mirror of the live fanout: a row of chips for the session's
// live subagents/bash jobs, shown above the composer. Each chip: spinner +
// name + what it's doing + mono time. It is CONNECTED in 5J: the container
// (ConversationScreen) derives `agents` from the session via
// liveTrayAgents(session) — the SAME rule the stream projection uses — so the
// tray never invents which subagents are alive.
//
// Chip descriptor (from liveTrayAgents): { id, kind:'subagent'|'bash', name,
// accent?, action?, time? }.
//   • subagent → its fanout identity accent (sky/teal/mauve…) on spinner+name.
//   • bash     → NO identity accent (INC-22): neutral overlay1 spinner + mono
//                `bash` label; teal never doubles as "a bash job".
//
// Click a chip → onOpen(id) opens that subagent's SubagentView (INC-06). The
// spinner reuses the Spinner primitive (color variants distinguish parallel
// subagents; overlay1 is the neutral bash color).
//
// TODO(5J sticky mirror, INC-11/P5): the tray should only render when the
// parent FanoutBlock is OUT of viewport (IntersectionObserver on the fanout
// block, tray hidden while it's visible). Not wired yet — the tray is shown
// whenever there are live agents; the container gates it minimally. Prioritized
// connecting real data over the observer.
export function AgentTray({ agents = [], onOpen }) {
  if (!agents.length) return null;
  return (
    <div class="agent-tray">
      {agents.map((a) => {
        const isBash = a.kind === "bash";
        const accent = isBash ? "overlay1" : (a.accent || "blue");
        return (
          <button
            type="button"
            key={a.id}
            class={`agent-chip${isBash ? " bash" : ""}`}
            onClick={onOpen ? () => onOpen(a.id) : undefined}
          >
            <Spinner color={isBash ? "overlay1" : accent} />
            <span
              class="agent-who"
              style={isBash ? undefined : { color: `var(--${accent})` }}
            >
              {a.name}
            </span>
            {a.action && <span class="agent-what">{a.action}</span>}
            {a.time && <span class="agent-time">{a.time}</span>}
          </button>
        );
      })}
    </div>
  );
}
