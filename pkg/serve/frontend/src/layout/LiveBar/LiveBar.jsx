import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { Square } from "lucide-preact";
import { activityPhase, activityText, formatElapsed } from "../../data/util/activity.js";
import { StateDot } from "../../primitives/StateDot/StateDot.jsx";
import { LiveSentence } from "./LiveSentence.jsx";
import { waitsOnYou } from "../../data/owners-model.js";
import "./LiveBar.css";

// LiveBar — ONE bar of live work above the composer. Markup and CSS are the
// catalogue's (catalog/zones-lab.jsx `LiveZone`, zones-lab.css the `.zl-live*`
// block), MOVED here rather than imitated: the classes travelled with the
// rules, so the strip IS the accepted design instead of a translation of it.
// The catalogue imports this component now, which is what makes one definition
// rather than two.
//
// It is the merge of what used to be two stacked things fighting for the same
// slot (NowLine, the foreground activity phrase; LiveDock, the permanent home
// of async work). They said two verbs at once and pushed the transcript twice;
// now there is a single row with two fixed slots:
//
//   [ turn state / foreground sentence ............. ] [ tally ]
//
// The SENTENCE belongs to the foreground run whenever there is one. When the
// turn has ended, its own still line occupies that slot; background work never
// borrows it or rotates through it.
//
// The TALLY only exists while something async is alive, and it is the door to
// the panel. It speaks the owner row's state language
// (decisions/lenguaje-de-estado.md): the number and nothing else, never one
// mark per item, and the whole chip amber when something waits on you. Only a
// child session can wait -- a subagent or a command carries no such state. The
// panel holds one row per live thing (sessions, subagents, commands), grouped
// by kind, opening UPWARD so the bar stays where it was. Opening a row goes to its
// screen (a subagent's conversation, a background bash's output).
//
// What is NOT the catalogue's is everything the prototype never had, grafted
// on top: the real elapsed clock (origin = server-stamped runStartedAtMs),
// liveTrayAgents() for the background, `open`/`onToggle` so the caller persists
// the panel per session (session.dockOpen), `forceCompact` collapsing the panel
// while the mobile keyboard is up WITHOUT touching that stored preference, and
// the house rule that a missing datum hides its segment rather than drawing a
// zero.
//
// What the old now-line taught, kept verbatim: the bar is flex:none, so its
// presence PUSHES the transcript up instead of overlaying the composer; it is
// absent in repose (returns null); and while the run is parked on you it goes
// amber WITHOUT motion — animating something that is not moving would lie.
//
// STOP lives here, not in the composer. This row is the one that says the
// agent is working, so "stop it" is its verb; the composer holds what the
// owner is about to say. It used to sit in the composer next to the mic,
// where two red squares side by side meant opposite things. `onStop` is
// passed only by hosts that have a run to stop; the button exists only while
// the FOREGROUND sentence is the agent's own -- a parked run (waiting on you)
// or a background-only bar has nothing to stop from here. Two steps, like the
// subagent head's Stop: the first tap asks, the second stops, and it disarms
// on its own.

// foregroundLine decides WHAT the foreground says: the phrase, whether the run
// is parked on the user, and the elapsed counter (empty unless the agent is
// actually running). Pure, so the decision is testable without a DOM.
export function foregroundLine(session, nowMs) {
  const phase = activityPhase(session);
  if (!phase) return null;

  // liveLabel is the catalogue adapter's fixture phrase. Production never sets
  // it: activityText is the single source the TUI and this bar share.
  const text = session.liveLabel || activityText(session);
  if (!text) return null;

  const waiting = phase === "waiting";
  // Elapsed only for the running phases; waiting parks the run, so no
  // elapsed-as-work counter (mirrors the app's timerless "Waiting for you").
  const runStartedAtMs = session.runStartedAtMs || 0;
  const showTimer = !waiting && runStartedAtMs > 0 && (phase === "thinking" || phase === "working");
  const elapsed = showTimer ? formatElapsed(Math.max(0, nowMs - runStartedAtMs)) : "";

  return { text, waiting, elapsed, phase };
}

// liveBarModel decides the WHOLE bar: the foreground run owns its sentence.
// With no foreground, the line names the ended turn; background work is only
// ever represented by the tally and its panel. `tally.waiting` is what turns
// the chip amber. Null means repose — no bar, the
// transcript reclaims the space.
export function liveBarModel(session, agents, nowMs) {
  const list = Array.isArray(agents) ? agents : [];
  const fg = foregroundLine(session, nowMs);

  let sentence = null;
  if (fg) {
    sentence = { kind: "foreground", text: fg.text, waiting: fg.waiting, elapsed: fg.elapsed, phase: fg.phase };
  } else if (list.length) {
    sentence = { kind: "ended", waiting: false, elapsed: "" };
  }
  if (!sentence) return null;

  const tally = list.length
    ? { count: list.length, waiting: list.some((a) => a.kind === "session" && waitsOnYou(a.state)) }
    : null;
  return { sentence, tally };
}

export function panelHasOverflow({ scrollHeight, clientHeight }) {
  return scrollHeight > clientHeight;
}

export function LiveBar({
  session,
  agents = [],
  nowMs: nowMsProp,
  open: openProp,
  onToggle,
  onOpen,
  onStop,
  dense = false,
  forceCompact = false,
}) {
  const list = Array.isArray(agents) ? agents : [];
  const panelRows = list.map((agent) => `${agent.kind}:${agent.id}`).join(",");
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
  const panelRef = useRef(null);
  const [panelOverflows, setPanelOverflows] = useState(false);
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

  // The lower wash is a claim that rows are clipped. Measure the mounted panel
  // before paint so that claim is never shown for a list that fits. Its row
  // identity changes whenever background work starts or finishes, keeping it
  // current without a persistent observer.
  useLayoutEffect(() => {
    const panel = panelRef.current;
    const next = !!panel && panelHasOverflow(panel);
    setPanelOverflows((current) => current === next ? current : next);
  }, [openPanel, panelRows, dense]);

  useEffect(() => {
    if (!forceCompact) setForceCompactOverride(false);
  }, [forceCompact]);

  const model = liveBarModel(session, list, nowMs);

  // A LiveBar is keyed by its host's current session. On entering an idle
  // conversation with background work, unfold once; a later explicit fold is
  // not revisited until the user enters the conversation again.
  useEffect(() => {
    if (!fgActive && list.length && !expanded) setExpanded(true);
  // This is deliberately mount-only: live work starting while the user is
  // already in the conversation must not reopen a panel they folded.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const [stopArmed, setStopArmed] = useState(false);
  useEffect(() => {
    if (!stopArmed) return undefined;
    const t = setTimeout(() => setStopArmed(false), 2000);
    return () => clearTimeout(t);
  }, [stopArmed]);

  if (!model) return null;

  const canStop = !!onStop && model.sentence.kind === "foreground" && !model.sentence.waiting;
  const stop = () => {
    if (!stopArmed) { setStopArmed(true); return; }
    setStopArmed(false);
    onStop();
  };

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
  const sessions = list.filter((a) => a.kind === "session");
  const subs = list.filter((a) => a.kind === "subagent");
  const bashes = list.filter((a) => a.kind === "bash");
  const waiting = sentence.waiting;
  const summary = backgroundSummary(list);

  return (
    <div class={`zl-live${dense ? " is-dense" : ""}${openPanel ? " is-open" : ""}${sentence.kind === "ended" ? " is-ended" : ""}`}>
      {openPanel && (
        <div ref={panelRef} class={`zl-live-panel${panelOverflows ? " has-overflow" : ""}`} role="region" aria-label="Live in the background">
          {[["Sessions", sessions], ["Subagents", subs], ["Commands", bashes]].map(([title, items]) => items.length > 0 && (
            <div class="zl-live-grp" key={title}>
              <div class="zl-group">
                <span>{title}</span>
                <span class="zl-group-n zl-data">{items.length}</span>
              </div>
              {items.map((a) => <LiveRow key={a.id} agent={a} onOpen={onOpen} />)}
            </div>
          ))}
        </div>
      )}

      <div class="zl-live-bar">
        {sentence.kind === "foreground" ? (
          <div class={`zl-live-now${waiting ? " is-waiting" : ""}`} role="status" aria-live="polite">
            <span class={`zl-live-dot is-${sentence.phase || "working"}`} aria-hidden="true" />
            {/* LiveSentence owns the shimmer and the cross-fade: it keeps the
                outgoing words on screen while the new ones arrive, which is
                what makes the change visible at all. Shimmering only while
                something runs -- never on "Waiting for you". */}
            <LiveSentence
              class="zl-live-txt"
              text={sentence.text}
              shimmer={!waiting && sentence.phase !== "waiting"}
            />
            {!!sentence.elapsed && <span class="zl-live-el zl-data">{sentence.elapsed}</span>}
          </div>
        ) : (
          <div class="zl-live-now is-ended" role="status" aria-live="polite">
            <span class="zl-live-ended-mark" aria-hidden="true" />
            <span class="zl-live-ended-title">Turn ended</span>
            <span class={`zl-live-ended-summary${openPanel ? " is-silent" : ""}`} aria-hidden={openPanel}>
              {summary}
            </span>
          </div>
        )}

        {tally && (
          <button
            type="button"
            class={`zl-live-tally${tally.waiting ? " is-waiting" : ""}`}
            onClick={toggle}
            aria-expanded={openPanel}
            aria-label={`${tally.count} in the background${tally.waiting ? ", something waits on you" : ""}${openPanel ? ", collapse" : ", expand"}`}
          >
            <span class="zl-live-n zl-data">{tally.count}</span>
            <ChevIcon up={!openPanel} />
          </button>
        )}

        {canStop && (
          <button
            type="button"
            class={`zl-live-stop${stopArmed ? " is-armed" : ""}`}
            onClick={stop}
            aria-label={stopArmed ? "Confirm stop" : "Stop the run"}
            title={stopArmed ? "Tap again to stop the run" : "Stop — ends the run (Esc in the composer)"}
          >
            <Square size={11} fill="currentColor" aria-hidden="true" />
            <span>{stopArmed ? "sure?" : "Stop"}</span>
          </button>
        )}
      </div>
    </div>
  );
}

function backgroundSummary(agents) {
  const sessions = agents.filter((agent) => agent.kind === "session").length;
  const subs = agents.filter((agent) => agent.kind === "subagent").length;
  const commands = agents.filter((agent) => agent.kind === "bash").length;
  const parts = [];
  if (sessions) parts.push(`${sessions} session${sessions === 1 ? "" : "s"}`);
  if (subs) parts.push(`${subs} subagent${subs === 1 ? "" : "s"}`);
  if (commands) parts.push(`${commands} command${commands === 1 ? "" : "s"}`);
  return parts.join(" · ");
}

// LiveId — the identity mark of a background item. A subagent gets its fanout
// accent (identity, never a state colour); a command gets a mono `$`, which
// says "shell" without borrowing a hue that would mean something else. The
// catalogue's model chips reuse this mark with a hue (`--h`);
// production agents carry a named accent (`--sky` etc.).
function LiveId({ agent }) {
  if (agent.kind === "session") {
    return <StateDot state={agent.state || "running"} size={7} aria-hidden="true" />;
  }
  if (agent.kind === "subagent") {
    return <span class="zl-live-id is-agent" style={liveIdStyle(agent)} aria-hidden="true" />;
  }
  return <span class="zl-live-id is-bash zl-data" aria-hidden="true">$</span>;
}

function ChevIcon({ up }) {
  return (
    <svg class={`zl-live-chev${up ? " is-up" : ""}`} viewBox="0 0 12 12" aria-hidden="true">
      <path d="M2.5 4.5L6 8l3.5-3.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
  );
}

function LiveRow({ agent, onOpen }) {
  // Both kinds open a detail view: a subagent its conversation (where you can
  // steer it), a background bash its read-only output (where you can only stop
  // it). What each row IS stays readable from the row itself, not from whether
  // it can be tapped.
  const openable = !!onOpen;
  const Tag = openable ? "button" : "div";
  const isBash = agent.kind === "bash";
  const isSession = agent.kind === "session";
  return (
    <Tag
      class="zl-live-row"
      type={openable ? "button" : undefined}
      onClick={openable ? () => onOpen(agent.id, agent.kind) : undefined}
      aria-label={openable
        ? (isBash ? `Show output of ${agent.action || agent.name}` : isSession ? `Open session ${agent.name}` : `Open subagent ${agent.name}`)
        : undefined}
    >
      <LiveId agent={agent} />
      <span class="zl-live-row-main">
        {isBash ? (
          <span class="zl-live-row-t zl-data">{agent.action || agent.name}</span>
        ) : (
          <>
            <span class="zl-live-row-t">{agent.name}{!!agent.task && <> <span class="zl-live-row-task">· {agent.task}</span></>}</span>
            {!!agent.action && <span class="zl-live-row-d">{agent.action}</span>}
          </>
        )}
      </span>
      {!!agent.time && <span class="zl-live-el zl-data">{agent.time}</span>}
      {openable && (
        <svg class="zl-live-go" viewBox="0 0 12 12" aria-hidden="true">
          <path d="M4 2.5L7.5 6 4 9.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      )}
    </Tag>
  );
}

function liveIdStyle(agent) {
  if (agent.kind !== "subagent") return undefined;
  if (agent.accent) return { "--zl-live-accent": `var(--${agent.accent})` };
  if (agent.hue != null) return { "--h": agent.hue };
  return undefined;
}
