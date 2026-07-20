import { useState } from "preact/hooks";
import { ChevronUp, ChevronDown, ChevronRight } from "lucide-preact";
import "./LiveDock.css";
import { Spinner } from "../../primitives/index.js";
import { useSpotlight } from "./use-live-dock.js";

// LiveDock — the persistent window onto live async work
// (SUBAGENTS-PERSISTENT-SPEC). A bar anchored between the stream and the
// composer, and the PERMANENT home for async work ("async in the dock, sync
// inline" — an async subagent or any background bash never blocks the
// conversation, so it never needs to live inline; it shows here for as long as
// it runs). Compact bar by default (identity dots + count + rotating
// spotlight); an expanded panel (one row per live thing, grouped by kind) on
// tap.
//
// Agents come from liveTrayAgents(session): { id, kind:'subagent'|'bash',
// name, accent?, action?, time? }. bash carries no fanout identity accent
// (INC-22): neutral --blue dot + mono `bash` label.
//
// `open`/`onToggle` make the expanded/collapsed state CONTROLLED, so the
// caller can persist it per session (store.sessions[id].dockOpen) instead of
// resetting every time the conversation is switched. When omitted, the dock
// falls back to its own local state (used by the catalog specimen, which has
// no session to persist against).
//
// `onOpen(id)` opens an async subagent's detail view (tap its row). Running
// async bash rows are informational only (no detail view; their output lands
// inline when they end). `forceCompact` collapses the panel to the bar
// (mobile keyboard open — writing wins, §1.5) WITHOUT touching the stored
// open/onToggle preference — it's a display-only override so the keyboard
// closing restores whatever the user had chosen.
export function LiveDock({ agents = [], onOpen, open: openProp, onToggle, forceCompact = false }) {
  const [localExpanded, setLocalExpanded] = useState(false);
  const controlled = onToggle != null;
  const expanded = controlled ? !!openProp : localExpanded;
  const setExpanded = (next) => {
    if (controlled) onToggle(typeof next === "function" ? next(expanded) : next);
    else setLocalExpanded(next);
  };
  const open = expanded && !forceCompact;

  // While the keyboard forces the bar compact, tapping it must NOT flip the
  // stored preference (you can't see the panel to intend it) — the bar just
  // can't expand right now. Toggling is a no-op until forceCompact clears.
  const toggle = () => {
    if (forceCompact) return;
    setExpanded((v) => !v);
  };

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
        onClick={toggle}
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
          {subs > 0 && <div class="grp">async subagents</div>}
          {agents
            .filter((a) => a.kind === "subagent")
            .map((a) => (
              <LiveRow key={a.id} agent={a} onOpen={onOpen} />
            ))}
          {bashes > 0 && <div class="grp">async jobs</div>}
          {agents
            .filter((a) => a.kind === "bash")
            .map((a) => (
              <LiveRow key={a.id} agent={a} onOpen={onOpen} />
            ))}
        </div>
      )}
    </div>
  );
}

function LiveRow({ agent, onOpen }) {
  const isBash = agent.kind === "bash";
  const accent = isBash ? "blue" : agent.accent || "blue";
  // Only async subagents have a detail view to open (tap the row → its
  // conversation). A running async bash has no detail view and no inline
  // surface (its full output lands inline as a delegation/history card when it
  // ends), so its row is informational: spinner + cmd + elapsed, not a button.
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
        {openable && (
          <span class="ld-open-chev" aria-hidden="true">
            <ChevronRight size={14} />
          </span>
        )}
      </MainTag>
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
