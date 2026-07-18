import { Check } from "lucide-preact";
import "./AgentTray.css";
import { Spinner } from "../../primitives/index.js";

// AgentTray — row of live subagent/tool chips, above the
// composer. Each chip: spinner + colored name + what it's doing + mono time.
// The spinner reuses the Spinner primitive (same pattern that used to live
// duplicated here as .agent-spinner, now tokenized with color variants
// to distinguish several agents in parallel — see FanoutBlock).
//
// state: "running" (default, spinner) | "done" (green check + pulsing peach
// "unseen" badge, Phase 3B — mirrors the finished-but-unread row of
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
