import { useEffect, useState } from "preact/hooks";
import { ChevronUp, ChevronDown, ChevronRight } from "lucide-preact";
import { activityPhase, activityText, formatElapsed } from "../../data/util/activity.js";
import "./LiveBar.css";

// LiveBar — ONE bar of live work above the composer, the merge of what used to
// be two stacked things fighting for the same slot (NowLine, the foreground
// activity phrase; LiveDock, the permanent home of async work). They said two
// verbs at once and pushed the transcript twice; now there is a single row with
// two fixed slots:
//
//   [ sentence ..................................... ] [ tally ]
//
// The SENTENCE belongs to the foreground run whenever there is one; only when
// the foreground is idle does the background take it over, with a spotlight
// rotating across the live async work. Never two verbs at a time.
//
// The TALLY only exists while something async is alive, and it is the door to
// the panel: one row per live thing (subagents, then commands), grouped by
// kind, opening UPWARD so the bar stays where it was. Opening a row goes to its
// screen (a subagent's conversation, a background bash's output).
//
// What the old now-line taught, kept verbatim: the bar is flex:none, so its
// presence PUSHES the transcript up instead of overlaying the composer; it is
// absent in repose (returns null); and while the run is parked on you it goes
// amber WITHOUT motion — animating something that is not moving would lie.
//
// The panel is capped and scrolls inside itself: N live jobs can never eat the
// conversation.
//
// Data: activityPhase / activityText / formatElapsed for the foreground (the
// same source as the TUI statusline, never duplicated), liveTrayAgents(session)
// for the background. `open`/`onToggle` keep the panel CONTROLLED so the caller
// persists it per session (session.dockOpen). `forceCompact` collapses the
// panel while the mobile keyboard is up (writing wins) WITHOUT touching that
// stored preference. `dense` is the pane dressing.

// foregroundLine decides WHAT the foreground says: the phrase, whether the run
// is parked on the user, and the elapsed counter (empty unless the agent is
// actually running). Pure, so the decision is testable without a DOM.
export function foregroundLine(session, nowMs) {
  const phase = activityPhase(session);
  if (!phase) return null;

  const text = activityText(session);
  if (!text) return null;

  const waiting = phase === "waiting";
  // Elapsed only for the running phases; waiting parks the run, so no
  // elapsed-as-work counter (mirrors the app's timerless "Waiting for you").
  const runStartedAtMs = session.runStartedAtMs || 0;
  const showTimer = !waiting && runStartedAtMs > 0 && (phase === "thinking" || phase === "working");
  const elapsed = showTimer ? formatElapsed(Math.max(0, nowMs - runStartedAtMs)) : "";

  return { text, waiting, elapsed, phase };
}

// liveBarModel decides the WHOLE bar: who owns the single sentence, and whether
// there is a tally at all. Null means repose — no bar, the transcript reclaims
// the space.
export function liveBarModel(session, agents, nowMs, spot = 0) {
  const list = Array.isArray(agents) ? agents : [];
  const fg = foregroundLine(session, nowMs);

  let sentence = null;
  if (fg) {
    sentence = { kind: "foreground", text: fg.text, waiting: fg.waiting, elapsed: fg.elapsed, phase: fg.phase };
  } else if (list.length) {
    // The background only speaks when the foreground is silent, so the bar
    // never carries two verbs.
    const agent = list[Math.min(Math.max(0, spot), list.length - 1)];
    sentence = { kind: "background", agent, waiting: false, elapsed: agent.time || "" };
  }
  if (!sentence) return null;

  return { sentence, tally: list.length ? { count: list.length, agents: list } : null };
}

// useSpotlight rotates an index across `count` items, so the bar can cycle
// through what each live thing is doing without expanding. Rotation stops (and
// pins index 0) under prefers-reduced-motion, with a single item, or when it is
// not the background's turn to speak.
export function useSpotlight(count, active, intervalMs = 4000) {
  const [index, setIndex] = useState(0);

  useEffect(() => {
    if (index >= count) setIndex(0);
  }, [count, index]);

  useEffect(() => {
    if (!active || count <= 1) {
      setIndex(0);
      return;
    }
    const reduced =
      typeof matchMedia !== "undefined" &&
      matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (reduced) return;
    const t = setInterval(() => setIndex((i) => (i + 1) % count), intervalMs);
    return () => clearInterval(t);
  }, [count, active, intervalMs]);

  return Math.min(index, Math.max(0, count - 1));
}

export function LiveBar({
  session,
  agents = [],
  nowMs: nowMsProp,
  open: openProp,
  onToggle,
  onOpen,
  dense = false,
  forceCompact = false,
}) {
  const list = Array.isArray(agents) ? agents : [];
  const fgActive = activityPhase(session) !== null;
  const alive = fgActive || list.length > 0;

  // Own clock, used only when the caller does not supply one (ConversationScreen
  // already runs a one-second tick). Hooks run unconditionally; the interval is
  // what we skip.
  const driven = nowMsProp != null;
  const [ownNowMs, setOwnNowMs] = useState(() => Date.now());
  useEffect(() => {
    if (driven || !alive) return;
    setOwnNowMs(Date.now());
    const t = setInterval(() => setOwnNowMs(Date.now()), 1000);
    return () => clearInterval(t);
  }, [driven, alive]);
  // The timer ORIGIN is always the server-stamped runStartedAtMs; the clock only
  // supplies "now".
  const nowMs = driven ? nowMsProp : ownNowMs;

  const [localExpanded, setLocalExpanded] = useState(false);
  const [forceCompactOverride, setForceCompactOverride] = useState(false);
  const controlled = onToggle != null;
  const expanded = controlled ? !!openProp : localExpanded;
  const setExpanded = (next) => {
    if (controlled) onToggle(typeof next === "function" ? next(expanded) : next);
    else setLocalExpanded(next);
  };
  // The keyboard state is inferred from the viewport and can briefly be stale
  // on iOS. A deliberate tap on this primary navigation control wins over that
  // heuristic, rather than leaving live work inaccessible until a reload.
  const openPanel = expanded && list.length > 0 && (!forceCompact || forceCompactOverride);

  useEffect(() => {
    if (!forceCompact) setForceCompactOverride(false);
  }, [forceCompact]);

  const spot = useSpotlight(list.length, !fgActive);
  const model = liveBarModel(session, list, nowMs, spot);

  if (!model) return null;

  // While the keyboard normally keeps the panel shut, an explicit tap is still
  // an unambiguous request to inspect the live work. This also provides a safe
  // escape hatch when Safari has not yet reported the keyboard closing.
  const toggle = () => {
    if (forceCompact && !openPanel) {
      // `expanded` may already be true behind the compact presentation. Make
      // the first tap reveal it rather than toggling that hidden preference off
      // and making the user tap a second time.
      setForceCompactOverride(true);
      setExpanded(true);
      return;
    }
    setExpanded((v) => !v);
  };

  const { sentence, tally } = model;
  const subs = list.filter((a) => a.kind === "subagent");
  const bashes = list.filter((a) => a.kind !== "subagent");

  return (
    <div class={`livebar${dense ? " is-dense" : ""}${openPanel ? " is-open" : ""}`}>
      {openPanel && (
        <div class="lb-panel" role="region" aria-label="Live in the background">
          {[["Subagents", subs], ["Commands", bashes]].map(([title, items]) => items.length > 0 && (
            <div class="lb-grp" key={title}>
              <div class="lb-grp-h">
                <span>{title}</span>
                <span class="lb-grp-n">{items.length}</span>
              </div>
              {items.map((a) => <LiveRow key={a.id} agent={a} onOpen={onOpen} />)}
            </div>
          ))}
        </div>
      )}

      <div class="lb-bar">
        <div
          class={`lb-now${sentence.waiting ? " is-waiting" : ""}${sentence.kind === "background" ? " is-bg" : ""}`}
          role="status"
          aria-live="polite"
        >
          {sentence.kind === "foreground" ? (
            <>
              <span class={`lb-dot is-${sentence.waiting ? "waiting" : "working"}`} aria-hidden="true" />
              <span class="lb-txt">{sentence.text}</span>
            </>
          ) : (
            <span class="lb-spot" key={sentence.agent.id}>
              <LiveId agent={sentence.agent} />
              {sentence.agent.kind === "subagent" ? (
                <span class="lb-txt">
                  <span class="lb-who" style={accentStyle(sentence.agent)}>{sentence.agent.name}</span>
                  {sentence.agent.action ? ` · ${sentence.agent.action}` : ""}
                </span>
              ) : (
                <span class="lb-txt is-data">{sentence.agent.action || sentence.agent.name}</span>
              )}
            </span>
          )}
          {sentence.elapsed && <span class="lb-el">{sentence.elapsed}</span>}
        </div>

        {tally && (
          <button
            type="button"
            class="lb-tally"
            onClick={toggle}
            aria-expanded={openPanel}
            aria-label={`${tally.count} in the background${openPanel ? ", collapse" : ", expand"}`}
          >
            <span class="lb-dots" aria-hidden="true">
              {tally.agents.map((a) => <LiveId agent={a} key={a.id} />)}
            </span>
            <span class="lb-n">{tally.count}</span>
            <span class="lb-chev" aria-hidden="true">
              {openPanel ? <ChevronDown size={14} /> : <ChevronUp size={14} />}
            </span>
          </button>
        )}
      </div>
    </div>
  );
}

// LiveId — the identity mark of a background item. A subagent gets its fanout
// accent (identity, never a state colour); a command gets a mono `$`, which
// says "shell" without borrowing a hue that would mean something else. In the
// tally cluster the `$` becomes a neutral dot: two of them side by side read as
// a price.
function LiveId({ agent }) {
  if (agent.kind === "subagent") {
    return <span class="lb-id is-agent" style={accentVar(agent)} aria-hidden="true" />;
  }
  return <span class="lb-id is-bash" aria-hidden="true">$</span>;
}

function LiveRow({ agent, onOpen }) {
  // Both kinds open a detail view: a subagent its conversation (where you can
  // steer it), a background bash its read-only output (where you can only stop
  // it). What each row IS stays readable from the row itself, not from whether
  // it can be tapped.
  const openable = !!onOpen;
  const Tag = openable ? "button" : "div";
  const isBash = agent.kind !== "subagent";
  return (
    <Tag
      class={`lb-row${isBash ? " is-bash" : ""}`}
      type={openable ? "button" : undefined}
      onClick={openable ? () => onOpen(agent.id, agent.kind) : undefined}
      aria-label={openable
        ? (isBash ? `Show output of ${agent.action || agent.name}` : `Open subagent ${agent.name}`)
        : undefined}
    >
      <LiveId agent={agent} />
      <span class="lb-row-main">
        {isBash ? (
          <span class="lb-row-t is-data">{agent.action || agent.name}</span>
        ) : (
          <>
            <span class="lb-row-t" style={accentStyle(agent)}>{agent.name}</span>
            {agent.action && <span class="lb-row-d">{agent.action}</span>}
          </>
        )}
      </span>
      {agent.time && <span class="lb-el">{agent.time}</span>}
      {openable && <span class="lb-go" aria-hidden="true"><ChevronRight size={14} /></span>}
    </Tag>
  );
}

// The fanout accent is the subagent's IDENTITY, assigned by the stream model.
// bash carries none by design.
function accentStyle(agent) {
  if (agent.kind !== "subagent" || !agent.accent) return undefined;
  return { color: `var(--${agent.accent})` };
}

// The identity dot is a ::before, which an inline colour cannot reach, so the
// accent travels as a custom property the stylesheet reads.
function accentVar(agent) {
  if (agent.kind !== "subagent" || !agent.accent) return undefined;
  return { "--lb-accent": `var(--${agent.accent})` };
}
