import { useEffect, useState } from "preact/hooks";
import { Check, X as XIcon } from "lucide-preact";
import "./tally-lab.css";
/* CATALOG ONLY. Nothing here is imported by the product: this file mounts the
   SHIPPED LiveBar (layout/LiveBar) inside wrapper hosts, so every direction
   below is the real bar in a different frame, never a redrawing of it. The
   only new markup is the chrome each direction adds AROUND it — the piece
   production does not have (a finished-and-unread job has no representation in
   the bar at all today) and the hosts that move it somewhere else. */
import { LiveBar } from "../layout/LiveBar/LiveBar.jsx";
import { Composer } from "../layout/Composer/Composer.jsx";
import { MobileChrome } from "../layout/mobile/MobileChrome/MobileChrome.jsx";
import { UserWaypoint, AssistantDocument } from "../components/index.js";

/* ── The problem ───────────────────────────────────────────────────────────

   Read in the shipped component (LiveBar.jsx:81-90): when the foreground has
   no phrase, `liveBarModel` hands the SENTENCE SLOT to the background, and
   `useSpotlight` rotates it across the live async work every 4s. The row that
   comes out is the same row, in the same place, in the same voice as a running
   turn — same `.zl-live-now`, same 14/500 type, same elapsed counter pinned
   right — and it CHANGES CONTENT on a timer, which is the one thing that says
   "this is happening in front of you right now".

   So a stopped agent with three live subagents is drawn exactly like a working
   one. That is not an ugliness, it is a false statement, and the criterion for
   everything below is a single question:

     Opening the conversation cold, without touching anything, can the owner
     tell whether HIS TURN is running?

   Every direction here answers yes, and every one of them KILLS THE ROTATION:
   a single item narrated in the present, cycling, is the foreground's grammar
   no matter how it is styled. What they disagree about is what takes its
   place — a plural count, a marker, a different part of the screen, a list, or
   silence.

   Second finding, not in the original hypothesis: state 4 does not exist.
   `liveTrayAgents` keeps only `running`/`cancelling` jobs (stream-model.js),
   so a background command that finishes while the agent is stopped does not
   settle anywhere — the bar simply disappears. Today the owner never learns it
   ended. Each direction below has to say where an ending goes. */

const LONG_CMD =
  "exec env MOA_CONFIG_DIR=/tmp/moa-probe-8821 MOA_LOG=debug ./moa serve --port 7399 --verbose";

const IT = {
  terra: { id: "terra", kind: "subagent", name: "terra", accent: "teal", task: "review the diff", action: "Reading pkg/attach/store.go", ago: 134 },
  luna: { id: "luna", kind: "subagent", name: "luna", accent: "sky", task: "map the callers", action: "Reading pkg/serve/stream.go", ago: 78 },
  sol: { id: "sol", kind: "subagent", name: "sol", accent: "mauve", task: "the delete race", action: "Reading the index writer", ago: 212 },
  serve: { id: "serve", kind: "bash", name: "bash", cmd: LONG_CMD, short: "moa serve", ago: 252 },
  test: { id: "test", kind: "bash", name: "bash", cmd: "go test ./... -race", short: "go test ./…", ago: 41 },
  build: { id: "build", kind: "bash", name: "bash", cmd: "npm run build", short: "npm run build", ago: 12 },
};

const DONE_TEST = { id: "d-test", kind: "bash", name: "bash", short: "go test ./…", cmd: "go test ./... -race", ok: true, result: "ok · 1666 passed", ran: "2m 14s", endedAgo: 126 };
const DONE_LUNA = { id: "d-luna", kind: "subagent", name: "luna", accent: "sky", short: "luna", result: "12 call sites, 3 files", ok: true, ran: "1m 41s", endedAgo: 240 };
const DONE_BUILD = { id: "d-build", kind: "bash", name: "bash", short: "npm run build", cmd: "npm run build", ok: false, result: "exit 1 · type error in Sidebar.tsx", ran: "38s", endedAgo: 54 };

const FG = { text: "Running go vet ./...", ago: 37 };

export const TALLY_STATES = [
  {
    id: "working", n: 1, label: "Turn running",
    sub: "agent working · nothing async",
    fg: FG, live: [], done: [],
  },
  {
    id: "mixed", n: 2, label: "Turn running + async",
    sub: "agent working · 1 subagent + 1 command",
    fg: FG, live: [IT.terra, IT.test], done: [],
  },
  {
    id: "stopped", n: 3, label: "Turn stopped + 3 subagents",
    sub: "THE CASE · agent idle, three subagents alive, tally closed",
    fg: null, live: [IT.terra, IT.luna, IT.sol], done: [],
  },
  {
    id: "ended", n: 4, label: "Turn stopped + finished, unread",
    sub: "two background jobs ended while nobody was looking",
    fg: null, live: [], done: [DONE_TEST, DONE_LUNA],
  },
  {
    id: "crowd", n: 5, label: "Turn stopped · 2 subagents + 3 commands",
    sub: "and one of them already failed · the long command is here",
    fg: null, live: [IT.terra, IT.sol, IT.serve, IT.test, IT.build], done: [DONE_BUILD],
  },
];

/* ── Fixture plumbing ──────────────────────────────────────────────────── */

function useNow(active, frozen) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active || frozen) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [active, frozen]);
  return now;
}

function fixtureSession(fg, t0) {
  if (!fg) return { state: "idle" };
  return { state: "running", runStartedAtMs: t0 - fg.ago * 1000, liveLabel: fg.text };
}

function mins(sec) {
  if (sec < 60) return `${Math.round(sec)}s`;
  return `${Math.floor(sec / 60)}m`;
}

/* Each direction decides what a background item is CALLED and how its clock is
   worded. Both are plain strings in the shipped descriptor, so a direction can
   change the grammar without changing the component. */
function agentsFor(dir, items) {
  return items.map((it) => ({
    id: it.id,
    kind: it.kind,
    name: it.name,
    accent: it.accent,
    task: it.task,
    action: dir.label ? dir.label(it) : (it.kind === "subagent" ? it.action : it.cmd),
    time: dir.clock ? dir.clock(it) : mins(it.ago),
  }));
}

function Bar({ st, t0, now, dense, agents, open, onToggle }) {
  return (
    <LiveBar
      session={fixtureSession(st.fg, t0)}
      agents={agents}
      nowMs={now}
      dense={dense}
      open={open}
      onToggle={onToggle}
      onOpen={() => {}}
      onStop={() => {}}
    />
  );
}

/* ── Settled work ──────────────────────────────────────────────────────────
   The piece production does not have. One component, three tones, so the five
   directions differ in where an ending goes and how loudly it lands, not in
   five separate drawings of the same row. */
function Settled({ items, tone = "quiet", onDismiss }) {
  if (!items.length) return null;
  return (
    <div class={`tb-done is-${tone}`} role="region" aria-label="Finished in the background">
      {items.map((d) => (
        <div class={`tb-done-row${d.ok ? "" : " is-bad"}`} key={d.id}>
          <span class="tb-done-mark" aria-hidden="true">
            {d.ok ? <Check size={12} strokeWidth={2.5} /> : <XIcon size={12} strokeWidth={2.5} />}
          </span>
          <span class="tb-done-main">
            <span class="tb-done-t">{d.short}</span>
            <span class="tb-done-d">{d.result} · ran {d.ran}</span>
          </span>
          <span class="tb-done-el">{mins(d.endedAgo)} ago</span>
          <button type="button" class="tb-done-go">{d.kind === "subagent" ? "Read" : "Output"}</button>
          {onDismiss && (
            <button type="button" class="tb-done-x" aria-label="Dismiss" onClick={() => onDismiss(d.id)}>
              <XIcon size={12} strokeWidth={2.2} />
            </button>
          )}
        </div>
      ))}
    </div>
  );
}

function plural(n, one, many) {
  return `${n} ${n === 1 ? one : many}`;
}

// Plural and still, never a rotating singular. With both kinds alive the row
// counts jobs instead of listing two kinds: "2 subagents · 3 commands still
// running" is a true sentence that does not fit a 390 phone, and a truncated
// count is worse than a coarser one.
function shortPhrase(items) {
  const subs = items.filter((i) => i.kind === "subagent").length;
  const cmds = items.length - subs;
  if (subs && cmds) return plural(items.length, "job", "jobs");
  return countPhrase(items);
}

function countPhrase(items) {
  const subs = items.filter((i) => i.kind === "subagent").length;
  const cmds = items.length - subs;
  const parts = [];
  if (subs) parts.push(plural(subs, "subagent", "subagents"));
  if (cmds) parts.push(plural(cmds, "command", "commands"));
  return parts.join(" · ");
}

/* ══ A · The subject is the turn ═══════════════════════════════════════════
   The row never stops being about MY turn. Running, it is the shipped bar,
   untouched. Stopped, the bar says so in its own words — flat grey, no dot, no
   shimmer, no clock — and the background is a still plural count beside it,
   never a rotating sentence. The two readings differ by the one thing that is
   never shared: the breathing dot and a verb in the present belong to the turn
   and to nothing else. */
function DirA({ st, t0, now, dense }) {
  const [open, setOpen] = useState(false);
  const [done, setDone] = useState(st.done);
  useEffect(() => setDone(st.done), [st]);
  const idle = !st.fg;
  // With nothing live there is no bar to overlay, so the turn's line becomes
  // an ordinary row instead of an absolute one with nothing under it.
  const hasBar = st.live.length > 0 || !!st.fg;
  const agents = agentsFor(DIRS[0], st.live);
  return (
    <div class={`tb-v tb-a${idle ? " is-idle" : ""}${dense ? " is-dense" : ""}`}>
      <Settled items={done} tone="quiet" onDismiss={(id) => setDone((v) => v.filter((d) => d.id !== id))} />
      <div class={`tb-a-row${idle && hasBar ? " has-bar" : ""}`}>
        {idle && (
          <span class="tb-a-turn">
            <span class="tb-a-turn-s">Turn ended</span>
            {!!st.live.length && <span class="tb-a-bg">{shortPhrase(st.live)} still running</span>}
          </span>
        )}
        {(st.live.length > 0 || st.fg) && (
          <Bar st={st} t0={t0} now={now} dense={dense} agents={agents} open={open} onToggle={setOpen} />
        )}
      </div>
    </div>
  );
}

/* ══ B · A marker, not a sentence ══════════════════════════════════════════
   With no turn of my own, the zone contracts to the smallest thing that can
   still be opened: the shipped tally, alone at the right edge, no words. A
   sentence on that line therefore MEANS my turn is running — the left half of
   the row is reserved for it and is empty otherwise. An ending turns the same
   marker green with a count; tapping it unfolds what finished. */
function DirB({ st, t0, now, dense }) {
  const [open, setOpen] = useState(false);
  const [doneOpen, setDoneOpen] = useState(false);
  const [done, setDone] = useState(st.done);
  useEffect(() => { setDone(st.done); setDoneOpen(false); }, [st]);
  const idle = !st.fg;
  const agents = agentsFor(DIRS[1], st.live);
  return (
    <div class={`tb-v tb-b${idle ? " is-idle" : ""}${idle && st.live.length ? " is-collapsed" : ""}`}>
      {doneOpen && <Settled items={done} tone="quiet" onDismiss={(id) => setDone((v) => v.filter((d) => d.id !== id))} />}
      <div class="tb-b-row">
        {!!done.length && (
          <button type="button" class={`tb-b-mark${doneOpen ? " is-open" : ""}`} onClick={() => setDoneOpen((v) => !v)}>
            <span class="tb-b-mark-i" aria-hidden="true"><Check size={11} strokeWidth={2.6} /></span>
            <span class="tb-b-mark-n">{done.length}</span>
            <span class="tb-b-mark-w">done</span>
          </button>
        )}
        {(st.live.length > 0 || st.fg) && (
          <Bar st={st} t0={t0} now={now} dense={dense} agents={agents} open={open} onToggle={setOpen} />
        )}
      </div>
    </div>
  );
}

/* ══ C · Background work lives somewhere else ══════════════════════════════
   The strip above the composer becomes the turn's and only the turn's: if a
   row is there, the agent is working, full stop. Everything asynchronous moves
   under the header — the same place whether the turn runs or not, so nothing
   ever migrates into the turn's slot. Distance does the work that styling was
   failing to do. */
function CTop({ st }) {
  const [open, setOpen] = useState(false);
  const [done, setDone] = useState(st.done);
  useEffect(() => { setDone(st.done); setOpen(false); }, [st]);
  const agents = agentsFor(DIRS[2], st.live);
  if (!st.live.length && !done.length) return null;
  return (
    <div class="tb-c-strip">
      <button type="button" class="tb-c-head" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
        <span class="tb-c-dots" aria-hidden="true">
          {st.live.map((i) => (
            <span
              key={i.id}
              class={`tb-c-dot${i.kind === "subagent" ? " is-agent" : ""}`}
              style={i.accent ? { background: `var(--${i.accent})` } : undefined}
            />
          ))}
        </span>
        <span class="tb-c-txt">
          {st.live.length ? `${countPhrase(st.live)} in the background` : "Background work finished"}
        </span>
        {!!done.length && <span class="tb-c-done">{done.length} done</span>}
        <svg class={`tb-c-chev${open ? " is-open" : ""}`} viewBox="0 0 12 12" aria-hidden="true">
          <path d="M2.5 4.5L6 8l3.5-3.5" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </button>
      {open && (
        <div class="tb-c-body">
          <Settled items={done} tone="row" onDismiss={(id) => setDone((v) => v.filter((d) => d.id !== id))} />
          {!!agents.length && (
            <div class="tb-c-live">
              <LiveBar session={{ state: "idle" }} agents={agents} open onToggle={() => {}} onOpen={() => {}} />
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function DirC({ st, t0, now, dense }) {
  // The dock only ever carries the foreground. No agents are passed at all, so
  // the shipped model returns null in repose and the transcript takes the room.
  if (!st.fg) return <div class="tb-v tb-c is-idle" />;
  return (
    <div class="tb-v tb-c">
      <Bar st={st} t0={t0} now={now} dense={dense} agents={[]} />
    </div>
  );
}

/* ══ D · In repose the place is a ledger ═══════════════════════════════════
   A sentence and a list are different objects, and that is the whole idea: the
   turn speaks in sentences, the background is counted in rows. Stopped, the
   shipped panel is simply always open and the sentence row is gone; each row
   carries its own duration in the past-continuous ("running 4m"), and an ended
   one keeps its place in the same list with the tense changed. */
function DirD({ st, t0, now, dense }) {
  const [open, setOpen] = useState(false);
  const [done, setDone] = useState(st.done);
  useEffect(() => { setDone(st.done); setOpen(false); }, [st]);
  const idle = !st.fg;
  const agents = agentsFor(DIRS[3], st.live);
  return (
    <div class={`tb-v tb-d${idle ? " is-idle" : ""}`}>
      {idle && <Settled items={done} tone="row" onDismiss={(id) => setDone((v) => v.filter((d) => d.id !== id))} />}
      {(st.live.length > 0 || st.fg) && (
        <Bar
          st={st}
          t0={t0}
          now={now}
          dense={dense}
          agents={agents}
          open={idle ? true : open}
          onToggle={idle ? () => {} : setOpen}
        />
      )}
    </div>
  );
}

/* ══ E · Only the ending speaks ════════════════════════════════════════════
   Running in the background is not news, so with the turn stopped it gets no
   words at all: one thin thread above the composer, one tick per live thing,
   tappable to see them. Nothing wordy can be mistaken for a running turn
   because there is nothing wordy. What DOES speak is the end: a card that
   states the result and stays until it is read. */
function DirE({ st, t0, now, dense }) {
  const [open, setOpen] = useState(false);
  const [done, setDone] = useState(st.done);
  useEffect(() => { setDone(st.done); setOpen(false); }, [st]);
  const idle = !st.fg;
  const agents = agentsFor(DIRS[4], st.live);
  return (
    <div class={`tb-v tb-e${idle ? " is-idle" : ""}`}>
      <Settled items={done} tone="card" onDismiss={(id) => setDone((v) => v.filter((d) => d.id !== id))} />
      {idle && !!st.live.length && (
        <>
          {open && (
            <div class="tb-e-panel">
              <LiveBar session={{ state: "idle" }} agents={agents} open onToggle={() => {}} onOpen={() => {}} />
            </div>
          )}
          <button
            type="button"
            class={`tb-e-thread${open ? " is-open" : ""}`}
            onClick={() => setOpen((v) => !v)}
            aria-expanded={open}
            aria-label={`${st.live.length} running in the background`}
          >
            <span class="tb-e-line" aria-hidden="true">
              {st.live.map((i) => <span class="tb-e-tick" key={i.id} />)}
            </span>
          </button>
        </>
      )}
      {!!st.fg && <Bar st={st} t0={t0} now={now} dense={dense} agents={agents} />}
    </div>
  );
}

/* ── The five directions ───────────────────────────────────────────────── */

export const DIRS = [
  {
    id: "a", name: "A · The subject is the turn",
    tag: "the row is always about my turn; the background is a still count",
    Zone: DirA,
    label: (it) => (it.kind === "subagent" ? it.action : it.short),
    clock: (it) => mins(it.ago),
    glance: "A grey, dotless “Turn ended” where the verb used to be. The presence of the breathing dot is the answer, and it is never lent out.",
    ending: "A quiet settled line above the row, dismissible.",
  },
  {
    id: "b", name: "B · A marker, not a sentence",
    tag: "in repose it contracts to the tally alone; a sentence means the turn runs",
    Zone: DirB,
    label: (it) => (it.kind === "subagent" ? it.action : it.short),
    clock: (it) => mins(it.ago),
    glance: "An empty line with a small chip at the right edge. Nothing narrates, so nothing can be mistaken for a run.",
    ending: "The same marker, green, with a count; it unfolds what finished.",
  },
  {
    id: "c", name: "C · Somewhere else entirely",
    tag: "async work lives under the header; the strip above the composer is the turn's alone",
    Zone: DirC, Top: CTop,
    label: (it) => (it.kind === "subagent" ? it.action : it.short),
    clock: (it) => mins(it.ago),
    glance: "No strip above the composer at all — that space is the turn's, and it is empty. The background is a line under the header, out of the turn's grammar.",
    ending: "The same top strip, saying what ended, until it is opened.",
  },
  {
    id: "d", name: "D · The place becomes a ledger",
    tag: "the turn speaks in sentences; the background is counted in rows",
    Zone: DirD,
    label: (it) => (it.kind === "subagent" ? it.action : it.short),
    clock: (it) => `running ${mins(it.ago)}`,
    glance: "A list with a header, not a sentence. A list is a different object; a running turn never draws one.",
    ending: "The row stays in the same list with the tense changed.",
  },
  {
    id: "e", name: "E · Only the ending speaks",
    tag: "wordless while it runs, loud when it lands",
    Zone: DirE,
    label: (it) => (it.kind === "subagent" ? it.action : it.short),
    clock: (it) => mins(it.ago),
    glance: "A thread of ticks and no words. There is no sentence to misread.",
    ending: "A full result card that persists until dismissed — the loudest of the five, on purpose.",
  },
];

/* ══ What ships today ══════════════════════════════════════════════════════
   The shipped component with nothing around it and the rotation running, so
   the pair can be judged against the thing it is meant to replace rather than
   against a memory of it. */
function DirToday({ st, t0, now, dense }) {
  const [open, setOpen] = useState(false);
  return (
    <div class="tb-v tb-now">
      <Bar
        st={st}
        t0={t0}
        now={now}
        dense={dense}
        agents={st.live.map((it) => ({
          id: it.id, kind: it.kind, name: it.name, accent: it.accent, task: it.task,
          action: it.kind === "subagent" ? it.action : it.cmd,
          time: mins(it.ago),
        }))}
        open={open}
        onToggle={setOpen}
      />
    </div>
  );
}

const TODAY = {
  id: "today", name: "Today · what ships",
  tag: "the background takes the turn's line and rotates across it every 4s",
  Zone: DirToday,
  glance: "A sentence with a coloured mark and a ticking clock that CHANGES every four seconds. Indistinguishable from a running turn — this is the bug.",
  ending: "Nothing. The job leaves liveTrayAgents and the bar disappears; the ending is never reported.",
};

// The picker offers today as a sixth choice, so the comparison is one tap away
// rather than a matter of remembering. The five directions stay five.
const PICKS = [...DIRS, TODAY];

/* ── Hosts ─────────────────────────────────────────────────────────────── */

function TranscriptTail() {
  return (
    <div class="tb-stream">
      <UserWaypoint time="09:31">
        <p>Mira la carrera del borrado en el store de adjuntos y pasa vet antes de dar por bueno el cambio.</p>
      </UserWaypoint>
      <AssistantDocument>
        <p>
          Confirmado: es una carrera en el borrado. He movido la comprobación dentro del lock y
          añadido un test que lanza cien borrados en paralelo.
        </p>
        <p>He repartido el resto: la revisión del diff, el mapa de llamadas y la causa del bloqueo.</p>
      </AssistantDocument>
    </div>
  );
}

function Phone({ dir, st, t0, now, tag }) {
  const Zone = dir.Zone;
  const Top = dir.Top;
  return (
    <div class="tb-phone-wrap">
      {tag && <div class="tb-tag">{tag}</div>}
      <div class="zl-phone tb-phone" data-tally={`${dir.id}-${st.id}`}>
        <TranscriptTail />
        <MobileChrome
          title="Carrera en el borrado"
          attention={{}}
          inboxCount={0}
          below={Top ? <Top st={st} /> : undefined}
        />
        <div class="zl-dock tb-dock">
          <Zone st={st} t0={t0} now={now} />
          <Composer />
        </div>
      </div>
    </div>
  );
}

function Desk({ dir, st, t0, now }) {
  const Zone = dir.Zone;
  const Top = dir.Top;
  return (
    <div class="tb-desk" data-tally={`desk-${dir.id}-${st.id}`}>
      {Top && <div class="tb-desk-top"><Top st={st} /></div>}
      <div class="tb-desk-stream"><TranscriptTail /></div>
      <div class="tb-desk-dock">
        <Zone st={st} t0={t0} now={now} />
        <Composer />
      </div>
    </div>
  );
}

/* ── The lab ───────────────────────────────────────────────────────────── */

export function TallyLab() {
  const params = typeof location !== "undefined" ? new URLSearchParams(location.search) : new URLSearchParams();
  const shots = params.get("shots");
  const [dirId, setDirId] = useState(params.get("dir") || "a");
  const [stId, setStId] = useState(params.get("state") || "stopped");
  const [t0] = useState(() => Date.now());
  const now = useNow(true, !!shots);
  const dir = PICKS.find((d) => d.id === dirId) || PICKS[0];
  const st = TALLY_STATES.find((s) => s.id === stId) || TALLY_STATES[2];

  // The pair that settles it: the same frame, stopped-with-three-subagents
  // beside a turn that is really running.
  if (shots === "pair") {
    return (
      <div class="zl tb tb-shots">
        {DIRS.map((d) => (
          <div class="tb-pair" data-pair={d.id} key={d.id}>
            <div class="tb-pair-h">{d.name}</div>
            <div class="tb-pair-row">
              <Phone dir={d} st={TALLY_STATES[2]} t0={t0} now={t0} tag="turn STOPPED · 3 subagents alive" />
              <Phone dir={d} st={TALLY_STATES[0]} t0={t0} now={t0} tag="turn RUNNING" />
            </div>
          </div>
        ))}
      </div>
    );
  }

  if (shots === "today") {
    return (
      <div class="zl tb tb-shots">
        <div class="tb-pair" data-pair="today">
          <div class="tb-pair-h">Today · what ships</div>
          <div class="tb-pair-row">
            <Phone dir={TODAY} st={TALLY_STATES[2]} t0={t0} now={t0} tag="turn STOPPED · 3 subagents alive" />
            <Phone dir={TODAY} st={TALLY_STATES[0]} t0={t0} now={t0} tag="turn RUNNING" />
          </div>
        </div>
      </div>
    );
  }

  if (shots === "state") {
    return (
      <div class="zl tb tb-shots">
        {TALLY_STATES.map((s) => (
          <div class="tb-sheet" data-sheet={s.id} key={s.id}>
            <div class="tb-sheet-h">{s.n} · {s.label} <span>{s.sub}</span></div>
            <div class="tb-sheet-row">
              {DIRS.map((d) => <Phone dir={d} st={s} t0={t0} now={t0} tag={d.name} key={d.id} />)}
            </div>
          </div>
        ))}
      </div>
    );
  }

  return (
    <div class="zl tb">
      <header class="tb-head">
        <h1>moa studio · <em>background work when the turn is stopped</em></h1>
        <p>
          Today the background borrows the foreground's line, in the foreground's voice, rotating
          every four seconds — so a stopped agent with three live subagents is drawn like a working
          one. Five directions, one criterion: <b>opening the conversation cold, can you tell whether
          YOUR turn is running?</b> All five remove the rotation; they disagree about what replaces it.
        </p>
      </header>

      <div class="tb-bars">
        <div class="tb-seg" role="group" aria-label="Direction">
          {PICKS.map((d) => (
            <button
              type="button"
              key={d.id}
              class={d.id === dir.id ? "is-on" : ""}
              onClick={() => setDirId(d.id)}
            >
              {d.id === "today" ? "today" : d.id.toUpperCase()}
            </button>
          ))}
        </div>
        <div class="tb-seg" role="group" aria-label="State">
          {TALLY_STATES.map((s) => (
            <button
              type="button"
              key={s.id}
              class={s.id === st.id ? "is-on" : ""}
              onClick={() => setStId(s.id)}
            >
              {s.n}
            </button>
          ))}
        </div>
      </div>

      <div class="tb-legend">
        <div class="tb-legend-d"><b>{dir.name}</b> — {dir.tag}</div>
        <div class="tb-legend-s"><b>{st.n} · {st.label}</b> — {st.sub}</div>
        <div class="tb-legend-g"><b>At a glance:</b> {dir.glance} <b>When it ends:</b> {dir.ending}</div>
      </div>

      <div class="tb-stage">
        <Phone dir={dir} st={st} t0={t0} now={now} tag="390 × 780" />
        <Desk dir={dir} st={st} t0={t0} now={now} />
      </div>
    </div>
  );
}
