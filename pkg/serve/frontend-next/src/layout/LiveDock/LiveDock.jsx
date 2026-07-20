import { useState } from "preact/hooks";
import { ChevronUp, ChevronDown, ArrowUp } from "lucide-preact";
import "./LiveDock.css";
import { Spinner } from "../../primitives/index.js";
import { useSpotlight } from "./use-live-dock.js";

// LiveDock — the persistent window onto live async work
// (SUBAGENTS-PERSISTENT-SPEC). A bar anchored between the stream and the
// composer that mirrors the RUNNING rows of the delegation block (and the
// parent's async bash) when their inline surface is scrolled out of view. It
// evolves AgentTray: compact bar (identity dots + count + rotating spotlight)
// by default, an expanded panel (one row per live thing + bash tail) on tap.
//
// Agents come from liveTrayAgents(session): { id, kind:'subagent'|'bash',
// name, accent?, action?, time? }. bash carries no fanout identity accent
// (INC-22): neutral --blue dot + mono `bash` label.
//
// `onOpen(id)` opens a subagent's detail view; `onJump(id)` scrolls the stream
// to that thing's inline point; `forceCompact` collapses the panel to the bar
// (mobile keyboard open — writing wins, §1.5).
export function LiveDock({ agents = [], onOpen, onJump, forceCompact = false }) {
  const [expanded, setExpanded] = useState(false);
  const open = expanded && !forceCompact;

  const subs = agents.filter((a) => a.kind === "subagent").length;
  const bashes = agents.length - subs;
  const spot = useSpotlight(agents.length);

  if (!agents.length) return null;

  const dotAccent = (a) => (a.kind === "bash" ? "blue" : a.accent || "blue");
  const active = agents[Math.min(spot, agents.length - 1)];

  return (
    <div class={`live-dock${open ? " open" : ""}`}>
      <button
        type="button"
        class="ld-bar"
        onClick={() => setExpanded((v) => !v)}
        aria-expanded={open}
        aria-label={open ? "Collapse live agents" : "Expand live agents"}
      >
        <span class="ld-dots" aria-hidden="true">
          {agents.map((a, i) => (
            <span
              key={a.id}
              class="ld-dot"
              style={{ background: `var(--${dotAccent(a)})`, animationDelay: `${i * 300}ms` }}
            />
          ))}
        </span>
        <span class="ld-count">{countLabel(subs, bashes)}</span>
        {!open && active && (
          <span class="ld-spot" key={active.id}>
            <span class="ld-spot-name" style={spotColor(active)}>
              {active.name}
            </span>
            {active.action && <span class="ld-spot-act">▸ {active.action}</span>}
            {active.time && <span class="ld-spot-time">{active.time}</span>}
          </span>
        )}
        <span class="ld-chev" aria-hidden="true">
          {open ? <ChevronDown size={14} /> : <ChevronUp size={14} />}
        </span>
      </button>

      {open && (
        <div class="ld-panel">
          {agents.map((a) => (
            <LiveRow key={a.id} agent={a} onOpen={onOpen} onJump={onJump} />
          ))}
        </div>
      )}
    </div>
  );
}

function LiveRow({ agent, onOpen, onJump }) {
  const isBash = agent.kind === "bash";
  const accent = isBash ? "blue" : agent.accent || "blue";
  // Only subagents have a detail view; bash rows aren't openable (their tail is
  // read at their inline point — jump to it). So the main cell is a button only
  // for subagents; for bash it's a static cell and the ↑ jump is the action.
  const openable = !isBash && !!onOpen;
  const MainTag = openable ? "button" : "div";
  return (
    <div class={`ld-row${isBash ? " bash" : ""}`}>
      <MainTag
        class="ld-row-main"
        type={openable ? "button" : undefined}
        onClick={openable ? () => onOpen(agent.id) : undefined}
      >
        <Spinner color={accent} size={11} />
        <span class="ld-row-name" style={isBash ? undefined : { color: `var(--${accent})` }}>
          {agent.name}
        </span>
        {agent.action && <span class="ld-row-act">▸ {agent.action}</span>}
        {agent.time && <span class="ld-row-time">{agent.time}</span>}
      </MainTag>
      {onJump && (
        <button
          type="button"
          class="ld-jump"
          onClick={() => onJump(agent.id)}
          aria-label="Jump to this in the conversation"
        >
          <ArrowUp size={14} />
        </button>
      )}
    </div>
  );
}

function countLabel(subs, bashes) {
  const parts = [];
  if (subs > 0) parts.push(`${subs} agent${subs === 1 ? "" : "s"}`);
  if (bashes > 0) parts.push(`${bashes} bash`);
  return parts.join(" · ") || "0 agents";
}

function spotColor(agent) {
  if (agent.kind === "bash") return undefined;
  return { color: `var(--${agent.accent || "blue"})` };
}
