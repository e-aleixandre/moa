import { useEffect, useState } from "preact/hooks";
import "./tally2-lab.css";
/* CATALOG ONLY — round two of ?view=tally. Nothing here is imported by the
   product: the three proposals mount the SHIPPED LiveBar (layout/LiveBar) in a
   wrapper host, exactly as round one did, and reuse round one's fixtures and
   frames (tally-lab.jsx) so the two rounds are comparable rather than merely
   similar. The only new markup is the chrome AROUND the bar. */
import { LiveBar } from "../layout/LiveBar/LiveBar.jsx";
import { IT, fixtureSession, mins, countPhrase, Settled, Phone, Desk } from "./tally-lab.jsx";

/* ── Where round one left it ───────────────────────────────────────────────

   Direction A is chosen: the SUBJECT of the row is my turn, never the
   background, and the four-second rotation dies. C is rejected — subagents and
   commands already have a place, and a place that moves with the state is not
   a place. So: nothing migrates. The bar stays where it is, the tally stays at
   the right edge, and the only question left is the one the owner put:

     "podemos dejar un mensaje estilo 'three subagents, two commands' … Pero
      entonces, si dejamos algo, ya no tiene sentido dejar desplegado el tally."

   A phrase that counts the background AND a list that names it are the same
   statement twice. The hypothesis tested here is that they are not two things
   at all, but ONE OBJECT IN TWO STATES:

     folded   → the line is the folded form of the list: a still plural count.
     unfolded → the list speaks; the line says nothing about the background.

   They can never both talk, and folding stops losing information. P1 is that
   hypothesis. P2 and P3 are the two ways of picking one voice instead of
   alternating: P2 keeps only the list (and opens it on entry, the owner's
   idea), P3 keeps only the phrase (and never auto-opens).

   ── The criterion has not changed ────────────────────────────────────────
   Opening the conversation cold, WITHOUT READING, is my turn running? So the
   three share one thing, and it is not a wording: a running turn is a blue
   breathing dot, a moving sentence and a ticking counter; a stopped turn is a
   flat grey DASH in the same slot, still, with no counter at all. Dot versus
   dash is a shape you recognise before you read it, and neither shape is ever
   lent to the background — that was A's whole point.

   ── State 4, the child's command ─────────────────────────────────────────
   Verified in production: when an async subagent or bash finishes with the
   agent stopped, the result enters as SendPrompt and STARTS A TURN by itself
   (pkg/serve/session_lifecycle.go:263-306 and 374-400). So "it ended and
   nobody knows" is not the normal case — the ending announces itself by making
   the dot blue again. The exception is a bash launched BY a subagent: it
   belongs to the child and never wakes the parent. That, plus the instant
   between the end and the turn starting, is the whole of state 4, and it is
   small: it settles as one row in the SAME list, tense changed, never a card
   and never a badge. `child` below is that state. */

const DONE_CHILD = {
  id: "d-child",
  kind: "bash",
  name: "bash",
  short: "go test ./…",
  cmd: "go test ./... -race",
  ok: true,
  result: "ok · 1666 passed · sol's",
  ran: "2m 14s",
  endedAgo: 96,
};

const FG = { text: "Running go vet ./...", ago: 37 };

// The owner's own frame: four subagents and one command, which is the shape
// that produced "SUBAGENTS 4 and the tally says five". The number is right
// (liveTrayAgents sums live subagents AND live bash jobs,
// data/stream-model.js:708-735); what he could not see was the COMMANDS
// section, which the panel's 208px cap pushes below the fold. Kept as a state
// so the three proposals can be judged in it rather than discussed.
const FABLE = { id: "fable", kind: "subagent", name: "fable", accent: "peach", task: "the unfold", action: "Drafting the motion", ago: 51 };

export const T2_STATES = [
  {
    id: "working", n: 1, label: "Turn running",
    sub: "the shape to beat · agent working, nothing async",
    fg: FG, live: [], done: [],
  },
  {
    id: "mixed", n: 2, label: "Turn running + async",
    sub: "agent working · 1 subagent + 1 command",
    fg: FG, live: [IT.terra, IT.test], done: [],
  },
  {
    id: "stopped", n: 3, label: "Turn stopped + 3 subagents",
    sub: "THE CASE · agent idle, three subagents alive",
    fg: null, live: [IT.terra, IT.luna, IT.sol], done: [],
  },
  {
    id: "child", n: 4, label: "Turn stopped + a child's command ended",
    sub: "the only ending that does NOT wake the parent turn",
    fg: null, live: [IT.terra, IT.sol], done: [DONE_CHILD],
  },
  {
    id: "crowd", n: 5, label: "Turn stopped · 2 subagents + 3 commands",
    sub: "the phrase at its longest, on a 390 phone",
    fg: null, live: [IT.terra, IT.sol, IT.serve, IT.test, IT.build], done: [],
  },
  {
    id: "his", n: 6, label: "Turn stopped · 4 subagents + 1 command",
    sub: "THE OWNER'S FRAME · the command sits below the panel's fold",
    fg: null, live: [IT.terra, IT.luna, IT.sol, FABLE, IT.test], done: [],
  },
];

/* ── Shared parts ──────────────────────────────────────────────────────── */

function agentsFor(items) {
  return items.map((it) => ({
    id: it.id,
    kind: it.kind,
    name: it.name,
    accent: it.accent,
    task: it.task,
    action: it.kind === "subagent" ? it.action : it.short,
    time: mins(it.ago),
  }));
}

/* The turn's own line, in repose. Same slot, same baseline, same left edge as
   the shipped sentence — nothing moves. What changes is the SHAPE of the mark
   and the absence of the counter. `phrase` is the folded background, and which
   proposals ever pass it is the whole experiment. */
function TurnLine({ phrase, speaking }) {
  return (
    <span class="t2-turn">
      <span class="t2-turn-mark" aria-hidden="true" />
      <span class="t2-turn-s">Turn ended</span>
      {/* Kept mounted and faded rather than unmounted: the words have to be on
          screen while they leave, and the line must not jump as they go. */}
      {!!phrase && <span class={`t2-turn-bg${speaking ? "" : " is-off"}`}>{phrase}</span>}
    </span>
  );
}

// Folded voice: plural and still, never a rotating singular, never a verb in
// the present — "3 subagents · 2 commands" is a count, not a narration. An
// ending that did not wake the turn (state 4) is appended in the same voice.
function foldedPhrase(live, done) {
  const parts = [];
  if (live.length) parts.push(countPhrase(live));
  if (done.length) parts.push(`${done.length} finished`);
  return parts.join(" · ");
}

/* One zone, three policies. The proposals differ ONLY in which state of the
   object is allowed to speak (`foldedVoice` / `unfoldedVoice`) and whether it
   opens on entry; everything else — the frame, the bar, the list, the mark —
   is deliberately identical, so a comparison is a comparison. */
function makeZone({ mode, foldedVoice, unfoldedVoice, opensOnEntry }) {
  return function Zone({ st, t0, now, dense }) {
    const idle = !st.fg;
    const hasLive = st.live.length > 0;
    const stage = st.stage;
    const [open, setOpen] = useState(() => stage != null ? stage > 0 : (opensOnEntry && idle && hasLive));
    const [done, setDone] = useState(st.done);
    // Entering the conversation again: `entry` is bumped by the lab's own
    // button, which is what re-plays the unfold without a reload. Folding it
    // afterwards sticks — for this visit only, which is what was decided.
    useEffect(() => {
      setDone(st.done);
      if (stage != null) { setOpen(stage > 0); return; }
      setOpen(opensOnEntry && idle && hasLive);
    }, [st.id, st.entry, stage]);

    const panelOpen = open && hasLive;
    // The phrase is computed whenever the proposal has any voice at all; which
    // STATE of the object gets to use it is the experiment.
    const speaking = panelOpen ? unfoldedVoice : foldedVoice;
    const phrase = foldedVoice || unfoldedVoice ? foldedPhrase(st.live, done) : "";
    // The settled row belongs to the list, so it shows where the list shows:
    // P1 and P3 fold it into the count, P2 folded says nothing about the
    // background at all, which is exactly its bet.

    return (
      <div
        class={`t2 t2-${mode}${idle ? " is-idle" : ""}${panelOpen ? " is-open" : ""}${stage != null ? ` is-stage${stage}` : ""}`}
        data-zone={mode}
      >
        {panelOpen && (
          <Settled
            items={done}
            tone="row"
            onDismiss={(id) => setDone((v) => v.filter((d) => d.id !== id))}
          />
        )}
        <div class={`t2-row${idle && hasLive ? " has-bar" : ""}`}>
          {idle && <TurnLine phrase={phrase} speaking={speaking} />}
          {(hasLive || st.fg) && (
            <LiveBar
              session={fixtureSession(st.fg, t0)}
              agents={agentsFor(st.live)}
              nowMs={now}
              dense={dense}
              open={open}
              onToggle={setOpen}
              onOpen={() => {}}
              onStop={() => {}}
            />
          )}
        </div>
      </div>
    );
  };
}

/* ── The three proposals ───────────────────────────────────────────────── */

export const T2_DIRS = [
  {
    id: "p1",
    name: "P1 · Folded speaks, unfolded is silent",
    tag: "the line and the list are one object in two states",
    Zone: makeZone({ mode: "p1", foldedVoice: true, unfoldedVoice: false, opensOnEntry: false }),
    folded: "The line carries the count: “Turn ended · 3 subagents”. Nothing is lost by folding, so folding is cheap.",
    unfolded: "The line drops the count and says only “Turn ended”. The list is the one voice; the two never overlap.",
    entry: "Folded. It does not need to open, because folded already says how much is running.",
    cost: "The one line changes wording when you fold it — a small animation to get right, and the only place where words move.",
  },
  {
    id: "p2",
    name: "P2 · Only the list (it opens on entry)",
    tag: "A plus the owner's idea, with no summary phrase at all",
    Zone: makeZone({ mode: "p2", foldedVoice: false, unfoldedVoice: false, opensOnEntry: true }),
    folded: "The line says only “Turn ended”. The background survives as the tally's dots and number, and nothing else.",
    unfolded: "The list names every live thing, with the child's finished command settled in it.",
    entry: "Opens already unfolded, with the panel's own unfold. Fold it and it stays folded — this visit only.",
    cost: "Folded, the background is a glyph you must decode. Worse: a state-4 ending is only visible while open.",
  },
  {
    id: "p3",
    name: "P3 · Only the phrase (never auto-opens)",
    tag: "A with the sentence tuned: plural, still, always there",
    Zone: makeZone({ mode: "p3", foldedVoice: true, unfoldedVoice: true, opensOnEntry: false }),
    folded: "Identical to P1 — the count on the line.",
    unfolded: "The count STAYS on the line above the list. The redundancy the owner spotted, visible here on purpose.",
    entry: "Folded, always. The list is only ever opened deliberately.",
    cost: "Open, it says the same thing twice; the wider the screen, the sillier it reads.",
  },
];

/* ── The lab ───────────────────────────────────────────────────────────── */

export function Tally2Lab() {
  const params = typeof location !== "undefined" ? new URLSearchParams(location.search) : new URLSearchParams();
  const shots = params.get("shots");
  const [dirId, setDirId] = useState(params.get("dir") || "p1");
  const [stId, setStId] = useState(params.get("state") || "stopped");
  const [entry, setEntry] = useState(0);
  const [t0] = useState(() => Date.now());
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (shots) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [shots]);

  const dir = T2_DIRS.find((d) => d.id === dirId) || T2_DIRS[0];
  const base = T2_STATES.find((s) => s.id === stId) || T2_STATES[2];
  const st = { ...base, entry };

  // The pair that decides it: the same frame, turn STOPPED with three live
  // subagents beside a turn that is really running. If the two do not separate
  // here without reading, the proposal has failed its only criterion.
  if (shots === "pair") {
    return (
      <div class="zl tb t2-shots">
        {T2_DIRS.map((d) => (
          <div class="tb-pair" data-pair={d.id} key={d.id}>
            <div class="tb-pair-h">{d.name}</div>
            {/* Three frames, not two: the hard comparison is not stopped
                against a bare run, it is stopped-with-work against a run that
                ALSO has background work — the case where both rows carry a
                tally and only the mark can separate them. */}
            <div class="tb-pair-row">
              <Phone dir={d} st={T2_STATES[2]} t0={t0} now={t0} tag="turn STOPPED · 3 subagents alive" />
              <Phone dir={d} st={T2_STATES[0]} t0={t0} now={t0} tag="turn RUNNING" />
              <Phone dir={d} st={T2_STATES[1]} t0={t0} now={t0} tag="turn RUNNING · with async too" />
            </div>
          </div>
        ))}
      </div>
    );
  }

  // The unfold, as a strip. Frames 1 and 3 are the real ends of the animation;
  // frame 2 is the shipped keyframe (zl-unfold: 6px, opacity) held still at
  // its middle, because a capture cannot hold a running animation.
  if (shots === "enter") {
    const p2 = T2_DIRS[1];
    return (
      <div class="zl tb t2-shots">
        <div class="tb-pair" data-pair="enter">
          <div class="tb-pair-h">P2 · entering the conversation (stopped, 3 subagents)</div>
          <div class="tb-pair-row">
            {[0, 1, 2].map((stage) => (
              <Phone
                key={stage}
                dir={p2}
                st={{ ...T2_STATES[2], stage, id: `stage${stage}` }}
                t0={t0}
                now={t0}
                tag={["1 · the frame arrives", "2 · the list unfolds", "3 · settled"][stage]}
              />
            ))}
          </div>
        </div>
      </div>
    );
  }

  if (shots === "state") {
    return (
      <div class="zl tb t2-shots">
        {T2_STATES.map((s) => (
          <div class="tb-sheet" data-sheet={s.id} key={s.id}>
            <div class="tb-sheet-h">{s.n} · {s.label} <span>{s.sub}</span></div>
            <div class="tb-sheet-row">
              {T2_DIRS.map((d) => <Phone dir={d} st={s} t0={t0} now={t0} tag={d.name} key={d.id} />)}
            </div>
          </div>
        ))}
      </div>
    );
  }

  return (
    <div class="zl tb t2-lab">
      <header class="tb-head">
        <h1>moa studio · <em>direction A, round two</em></h1>
        <p>
          A is chosen: the row is about MY turn and the rotation is gone. What is left is the
          redundancy — a phrase that counts the background and a list that names it say the same
          thing twice. <b>P1</b> makes them one object in two states (folded speaks, unfolded is
          silent); <b>P2</b> keeps only the list and opens it on entry; <b>P3</b> keeps only the
          phrase. In all three, a running turn is a breathing blue dot and a stopped one a flat
          grey dash: <b>the shape answers before the words do.</b>
        </p>
      </header>

      <div class="tb-bars">
        <div class="tb-seg" role="group" aria-label="Proposal">
          {T2_DIRS.map((d) => (
            <button type="button" key={d.id} class={d.id === dir.id ? "is-on" : ""} onClick={() => setDirId(d.id)}>
              {d.id.toUpperCase()}
            </button>
          ))}
        </div>
        <div class="tb-seg" role="group" aria-label="State">
          {T2_STATES.map((s) => (
            <button type="button" key={s.id} class={s.id === st.id ? "is-on" : ""} onClick={() => setStId(s.id)}>
              {s.n}
            </button>
          ))}
        </div>
        {/* The auto-unfold is a transition, so it has to be replayable with a
            thumb: this re-enters the conversation without a reload. */}
        <div class="tb-seg">
          <button type="button" class="t2-enter" onClick={() => setEntry((v) => v + 1)}>Enter again</button>
        </div>
      </div>

      <div class="tb-legend">
        <div class="tb-legend-d"><b>{dir.name}</b> — {dir.tag}</div>
        <div class="tb-legend-s"><b>{st.n} · {st.label}</b> — {st.sub}</div>
        <div class="tb-legend-g">
          <b>Folded:</b> {dir.folded} <b>Unfolded:</b> {dir.unfolded} <b>On entry:</b> {dir.entry}{" "}
          <b>What it costs:</b> {dir.cost}
        </div>
      </div>

      <div class="tb-stage">
        <Phone dir={dir} st={st} t0={t0} now={now} tag="390 × 780" />
        <Desk dir={dir} st={st} t0={t0} now={now} />
      </div>
    </div>
  );
}
