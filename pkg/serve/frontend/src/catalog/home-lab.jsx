import { Search, Plus, Inbox as InboxIcon, ChevronRight } from "lucide-preact";
import { Sidebar } from "../layout/Sidebar/Sidebar.jsx";
import { SessionRow, Dot } from "../components/SessionRow/SessionRow.jsx";
import { partitionByAttention } from "../data/util/project-sessions.js";
import "./home-lab.css";

// home-lab — CATALOG ONLY. Three directions for the phone's FIRST SCREEN, the
// one the owner lands on when no session is open.
//
// Nothing here is wired to production: `MobileConversationScreen`, `Sidebar`
// and `NewSessionSheet` are untouched. Direction A mounts the SHIPPED
// `<Sidebar density="phone"/>` — that is the whole point of it — under one
// lab-local wrapper class that repaints its host (sheet → canvas), the same
// technique title-attention-lab uses on MobileChrome. B and C are lab-local
// markup that mounts the shipped `SessionRow`, so the rows in all three
// directions are literally the same component.
//
// Every decision below cites SISTEMA-VISUAL.md §2 (P1…P5) or §1 (the three
// references that were actually looked at). Where the document has nothing to
// say, the note says so rather than inventing a principle.

/* ── Fixtures ──────────────────────────────────────────────────────────────
   The row shape is production's (Sidebar/sessions.js:toSpineRow): id, title,
   state, when, brief, briefTone, path, cwd, unseen. Four states, because the
   pretty empty one is the only one the current screen was ever judged in. */

const row = (o) => ({ when: "", brief: "", briefTone: "", path: "", cwd: "", ...o });

const PERM = row({
  id: "deploy",
  title: "deploy pulse api",
  state: "permission",
  when: "2m",
  brief: "Needs your answer",
  briefTone: "yellow",
  cwd: "/home/ealeixandre/dev/moa/pulse-api",
  detail: "kubectl apply -f deploy/pulse-api.yaml",
});
const RUNNING = row({
  id: "ws",
  title: "ws race fix",
  state: "running",
  when: "now",
  brief: "Running · 6m",
  briefTone: "neutral",
  cwd: "/home/ealeixandre/dev/moa/main",
  detail: "bun test src/layout",
});
const ERRORED = row({
  id: "sqlite",
  title: "migrate sqlite",
  state: "error",
  when: "22m",
  brief: "Stopped with an error",
  briefTone: "red",
  cwd: "/home/ealeixandre/dev/moa/migrate",
  detail: "exit 1 · schema.sql:41",
});
const UNSEEN = row({
  id: "frontend",
  title: "frontend polish",
  state: "idle",
  unseen: true,
  when: "14m",
  brief: "Answered · not read yet",
  briefTone: "mauve",
  cwd: "/home/ealeixandre/dev/moa/frontend-polish",
  detail: "3 commits, bundle rebuilt",
});

const saved = (id, title, when, dir) =>
  row({ id, title, state: "saved", when, path: `~/dev/moa/${dir}`, cwd: `/home/ealeixandre/dev/moa/${dir}` });

const SAVED_2 = [
  saved("s1", "ambient migration", "3h", "design-visual"),
  saved("s2", "release 0.37.4", "1d", "main"),
];

const SAVED_MANY = [
  ...SAVED_2,
  saved("s3", "split handlers", "1d", "refactor"),
  saved("s4", "pulse pairing copy", "2d", "pulse"),
  saved("s5", "attachment index leak", "2d", "main"),
  saved("s6", "heap pprof run", "3d", "debug"),
  saved("s7", "inbox event sources", "4d", "events"),
  saved("s8", "catalog fidelity 35", "5d", "design-visual"),
  saved("s9", "openai stall notes", "6d", "main"),
];

// The four states every direction is drawn in. `label` is what the contact
// sheet calls the column; `note` is why it is worth looking at.
export const HOME_STATES = [
  { id: "empty", label: "First run", note: "no sessions at all", active: [], saved: [] },
  { id: "few", label: "2 saved", note: "the owner's day today", active: [], saved: SAVED_2 },
  { id: "many", label: "11 sessions", note: "the list has to hold", active: [UNSEEN, ERRORED], saved: SAVED_MANY },
  {
    id: "waiting",
    label: "1 working · 1 waiting",
    note: "the state that tests P1",
    active: [PERM, RUNNING],
    saved: SAVED_2,
  },
];

const noop = () => {};
const VERSION = { current: "v0.37.4" };

/* Which sessions want you, and in what order, is NOT a decision this lab gets
   to make: it is production's `partitionByAttention` (permission, then error,
   then unread), the same predicate the Spine's "Needs attention" group uses.
   B and C both read it, so neither can rank a waiting session differently
   from the list the owner already knows. */
const attention = (state) => partitionByAttention(state.active).needs;
const working = (state) => partitionByAttention(state.active).rest;
const totalOf = (state) => state.active.length + state.saved.length;

/* ══ Direction A ═══════════════════════════════════════════════════════════
   THE LIST IS THE SCREEN. No home screen at all: with no session open, the
   phone mounts the SHIPPED Sidebar full width, no veil, no sheet. Zero new
   components, zero new tokens.

   Backed by: P1's consequence, written in the document ("the empty state of
   sessions is not a screen, it is the list of sessions"); §4.1(b), the defect
   this deletes — the same sessions drawn twice, one tap apart; and the memory
   `project/serve-redesign-coherence` (one canonical ledger at every density). */
function DirectionA({ state }) {
  return (
    <div class="home-a">
      <Sidebar
        density="phone"
        version={VERSION}
        active={state.active}
        saved={state.saved}
        newResults={[]}
        onSelectSession={noop}
        onNewSession={noop}
        onSearch={noop}
        onSettings={noop}
        inboxVisible
        inboxCount={state.id === "waiting" ? 1 : 0}
        onInbox={noop}
      />
    </div>
  );
}

/* ══ Direction B ═══════════════════════════════════════════════════════════
   A HOME SCREEN, ONE DOMINANT. Keeps a page of its own, but rebuilt to the
   rules: one big word as the title (Linear, §1.2), rows and not cards (P4),
   the roster truncated to what 390px holds with a text link for the rest (P5),
   and exactly ONE box on the screen — the mauve primary at the thumb (P3).
   The secondary is a text link; nothing has a dashed border.

   The two head icons are the Sidebar's own doors (search, inbox), so the
   product's navigation is not hidden the way §4.1(h) says it is today. */
function DirectionB({ state }) {
  const needs = attention(state);
  const work = working(state);
  const savedShown = state.saved.slice(0, needs.length + work.length > 0 ? 3 : 5);
  const savedHidden = state.saved.length - savedShown.length;
  /* Two groups, never merged: "needs you" and "is working" are different
     answers to P1's question, and a running session filed under Needs
     attention is the list lying about what is blocked. */
  const groups = [
    ["Needs attention", needs],
    ["Active", work],
  ].filter(([, list]) => list.length > 0);
  const empty = totalOf(state) === 0;
  return (
    <div class="home-b">
      <header class="hb-head">
        <h1 class="hb-title">Sessions</h1>
        {!empty && <span class="hb-count hl-data">{totalOf(state)}</span>}
        <button type="button" class="hb-ico" aria-label="Search"><Search size={18} /></button>
        <button type="button" class="hb-ico" aria-label="Inbox"><InboxIcon size={18} /></button>
      </header>

      <div class="hb-body">
        {empty ? (
          <p class="hb-blank">Nothing here yet. Start one below.</p>
        ) : (
          <>
            {groups.map(([label, list]) => (
              <div key={label}>
                <p class="hb-label">{label}</p>
                {list.map((s) => (
                  <SessionRow
                    key={s.id}
                    title={s.title}
                    state={s.state}
                    unseen={s.unseen}
                    when={s.when}
                    brief={s.brief}
                    briefTone={s.briefTone}
                    onClick={noop}
                  />
                ))}
              </div>
            ))}
            {savedShown.length > 0 && (
              <>
                <p class="hb-label">Saved</p>
                {savedShown.map((s) => (
                  <SessionRow key={s.id} title={s.title} state="saved" when={s.when} path={s.path} onClick={noop} />
                ))}
              </>
            )}
            {savedHidden > 0 && (
              <button type="button" class="hb-more">Show all {totalOf(state)} sessions</button>
            )}
          </>
        )}
      </div>

      {/* The only box on the screen, at the thumb. It does not move between
          states: first run and a full roster put it in the same place. */}
      <div class="hb-bar">
        <button type="button" class="hb-primary">
          <Plus size={18} aria-hidden="true" /> New session
        </button>
      </div>
    </div>
  );
}

/* ══ Direction C ═══════════════════════════════════════════════════════════
   DOES IT NEED YOU? The screen answers P1's first question before anything
   else, with the one thing on the canvas that has a surface (Raycast, §1.3:
   a single elevated object per screen). When nothing is waiting, that slot is
   not a box at all — it is one line — and the list rises into its place.

   The card is a DOOR, not a control: answering a permission from the home
   screen would change what the product does, which is the owner's call, not a
   mock-up's. It carries the same words the row carries (sessionRowReason) and
   the same colour contract (yellow waits, red failed, mauve unread). */
function NeedsCard({ s, more }) {
  const tone = s.state === "error" ? "red" : s.unseen ? "mauve" : "yellow";
  return (
    <>
      <button type="button" class={`hc-card tone-${tone}`}>
        <span class="hc-card-top">
          <Dot state={s.unseen ? "unseen" : s.state} />
          <span class="hc-card-kind">{s.brief}</span>
          <span class="hc-card-age hl-data">{s.when}</span>
        </span>
        <span class="hc-card-title">{s.title}</span>
        <span class="hc-card-detail hl-data">{s.detail}</span>
        <span class="hc-card-go">Open<ChevronRight size={14} aria-hidden="true" /></span>
      </button>
      {more > 0 && <button type="button" class="hc-more">{more} more waiting</button>}
    </>
  );
}

function DirectionC({ state }) {
  const needs = attention(state);
  const work = working(state);
  const empty = totalOf(state) === 0;
  const savedShown = state.saved.slice(0, needs.length ? 3 : 5);
  const savedHidden = state.saved.length - savedShown.length;
  return (
    <div class="home-c">
      <header class="hc-head">
        <span class="hc-word">moa</span>
        <button type="button" class="hc-ico" aria-label="Search"><Search size={18} /></button>
        <button type="button" class="hc-ico" aria-label="New session"><Plus size={18} /></button>
      </header>

      {needs.length > 0 ? (
        <div class="hc-answer">
          <NeedsCard s={needs[0]} more={needs.length - 1} />
        </div>
      ) : (
        <p class="hc-quiet">
          {/* Green is "inactive / ok", which is an answer about work that
              exists. On first run there is no work to be ok about, so the
              mark is omitted rather than recoloured. */}
          {!empty && <span class="hc-quiet-dot" aria-hidden="true" />}
          {empty
            ? "No sessions yet"
            : work.length > 0
              ? `Nothing needs you · ${work.length} still working`
              : "Nothing needs you"}
        </p>
      )}

      <div class="hc-rest">
        {empty ? (
          <button type="button" class="hc-first">
            <Plus size={18} aria-hidden="true" /> New session
          </button>
        ) : (
          <>
            {work.length > 0 && (
              <>
                <p class="hc-label">Working</p>
                {work.map((s) => (
                  <SessionRow key={s.id} title={s.title} state={s.state} when={s.when} brief={s.brief} briefTone={s.briefTone} onClick={noop} />
                ))}
              </>
            )}
            {savedShown.length > 0 && (
              <>
                <p class="hc-label">Where you left off</p>
                {savedShown.map((s) => (
                  <SessionRow key={s.id} title={s.title} state="saved" when={s.when} path={s.path} onClick={noop} />
                ))}
              </>
            )}
            {savedHidden > 0 && <button type="button" class="hc-more is-list">Show all {totalOf(state)} sessions</button>}
          </>
        )}
      </div>
    </div>
  );
}

/* ── The three, described in the words the decision needs ───────────────── */

export const HOME_DIRECTIONS = [
  {
    id: "a",
    name: "A · The list is the screen",
    one: "No home screen. The shipped Sidebar, full width, is what you land on.",
    principle:
      "P1's own consequence, written in §4.1: “the empty state of sessions is not a screen, it is the list of sessions”. Kills §4.1(b): the same sessions drawn twice, one tap apart.",
    wins: [
      "Zero new components and zero new tokens — it deletes code instead of adding it.",
      "One ledger at every density: the row you see here is the row the desktop spine shows.",
      "Search, +, inbox, version and settings are already where they always are.",
      "Row state colours (yellow / red / mauve) arrive for free, in every state.",
    ],
    costs: [
      "First run is chrome with a text link in it: the emptiest state gets the busiest header.",
      "The dominant action is a 44px “+” in the head — P3 is satisfied, generosity is not.",
      "“moa” is the title of the screen, so the screen never says what to do.",
      "Landing on a list means the product opens on a directory, never on a conversation.",
    ],
    Screen: DirectionA,
  },
  {
    id: "b",
    name: "B · One dominant, rows not cards",
    one: "A home page of its own, rebuilt to the rules: one big word, rows, one box.",
    principle:
      "P3 (one dominant; the secondary is text, never a second box), P4 (rows, not cards), P5 (the roster truncates instead of scrolling past the fold), Linear §1.2 for the single-word title.",
    wins: [
      "One box on the screen, at the thumb, in the same place in every state.",
      "Truncation is honest: “Show all 11 sessions” replaces a count that repeated what was above it (§4.1(f)).",
      "The dashed border is gone; nothing borrows the drop-target idiom (Pane.css:147).",
      "A title that names the place, not an absence.",
    ],
    costs: [
      "It is still a second drawing of the same list: the drawer is one tap away showing the same rows.",
      "New session takes the dominant box while P1 says creating comes last.",
      "Two screens to keep in step by hand — the defect §3 blames for the whole drift.",
      "“Needs attention” is a group label, so a permission looks like a row among rows.",
    ],
    Screen: DirectionB,
  },
  {
    id: "c",
    name: "C · Does it need you?",
    one: "The answer first, as the only object with a surface. Quiet when nothing waits.",
    principle:
      "P1 read literally (state before history before create), P2 (state is ambient, configuration is behind a tap), Raycast §1.3 (one elevated object per screen), GitHub §1.1 (the state chip's vocabulary).",
    wins: [
      "Answers the question the owner actually opens the phone with, in one glance.",
      "Elevation means one thing again: the only raised surface is the thing that stopped.",
      "When nothing waits it costs one line, and the list rises — no screen spent on an absence.",
      "The card carries the command that is blocked, which no row has room for.",
    ],
    costs: [
      "A fifth surface to maintain, and a second place where a waiting session is drawn.",
      "With nothing waiting it degrades towards B, so it must be judged in its quiet state too.",
      "The card is a door, not an answer: tapping it still lands you in the session (answering from here is a product decision, not a mock-up's).",
      "The document says nothing about how several waiting sessions stack — “1 more waiting” is a guess.",
    ],
    Screen: DirectionC,
  },
];

/* ── Lab chrome ────────────────────────────────────────────────────────────
   A 390×780 frame, scaled with a transform so the phone keeps its real
   proportions on whatever screen this page is read on (the same trick
   desktop-lab uses; type does not grow with a resized box). */

function Frame({ scale = 1, id, caption, sub, children }) {
  return (
    <figure class="home-fig" style={{ width: `${390 * scale}px` }}>
      <div class="home-slot" style={{ width: `${390 * scale}px`, height: `${780 * scale}px` }}>
        <div
          class="home-frame"
          data-home={id}
          style={{ transform: `scale(${scale})` }}
        >
          {children}
        </div>
      </div>
      {caption && (
        <figcaption class="home-cap">
          <b>{caption}</b>
          {sub && <span>{sub}</span>}
        </figcaption>
      )}
    </figure>
  );
}

/* ── Desktop ───────────────────────────────────────────────────────────────
   The phone's question, asked at 1280. The Spine here is the SHIPPED Sidebar
   at desktop density — the same component direction A mounts full-width on the
   phone — so what the frame shows is that A is what the desktop already does.
   What changes between the two frames is only the empty pane beside it. */

function DesktopFrame({ variant, caption, sub, scale }) {
  const state = HOME_STATES[3];
  const needs = attention(state);
  return (
    <figure class="home-fig is-desk" style={{ width: `${1280 * scale}px` }}>
      <div class="home-slot" style={{ width: `${1280 * scale}px`, height: `${800 * scale}px` }}>
        <div class="home-frame is-desk" data-home={`desk-${variant}`} style={{ transform: `scale(${scale})` }}>
          <div class="hd-shell">
            <div class="hd-spine">
              <Sidebar
                density="desktop"
                version={VERSION}
                active={state.active}
                saved={state.saved}
                newResults={[]}
                onSelectSession={noop}
                onNewSession={noop}
                onSearch={noop}
                onSettings={noop}
                inboxVisible
                inboxCount={1}
                onInbox={noop}
              />
            </div>
            <div class="hd-pane">
              {variant === "card" ? (
                <div class="hd-pane-inner">
                  <NeedsCard s={needs[0]} more={needs.length - 1} />
                </div>
              ) : (
                <p class="hd-pane-blank">Pick a session, or start one.</p>
              )}
            </div>
          </div>
        </div>
      </div>
      <figcaption class="home-cap"><b>{caption}</b><span>{sub}</span></figcaption>
    </figure>
  );
}

function ContactSheet({ stateId, scale }) {
  const state = HOME_STATES.find((s) => s.id === stateId) || HOME_STATES[3];
  return (
    <div class="home-strip">
      {HOME_DIRECTIONS.map(({ id, name, one, Screen }) => (
        <Frame key={id} id={`sheet-${id}-${state.id}`} scale={scale} caption={name} sub={one}>
          <Screen state={state} />
        </Frame>
      ))}
    </div>
  );
}

export function HomeLab() {
  const params = typeof location !== "undefined" ? new URLSearchParams(location.search) : new URLSearchParams();
  const only = params.get("only");
  const shots = params.get("shots") === "1";
  const dirs = only ? HOME_DIRECTIONS.filter((d) => d.id === only) : HOME_DIRECTIONS;

  // ?shots=1 — every frame at 1:1 in one grid, nothing else on the page. It is
  // what the capture script walks; the reading page below is for the phone.
  if (shots) {
    return (
      <div class="home-lab is-shots">
        {HOME_DIRECTIONS.map(({ id, Screen }) =>
          HOME_STATES.map((state) => (
            <Frame key={`${id}-${state.id}`} id={`${id}-${state.id}`} scale={1}>
              <Screen state={state} />
            </Frame>
          ))
        )}
      </div>
    );
  }

  return (
    <div class="home-lab">
      <header class="home-intro">
        <h1>The first screen, three ways</h1>
        <p>
          What the phone shows when no session is open. Every direction is drawn in the four states that
          matter — first run, the two-saved day, a full roster, and one session working while another waits
          — because the current screen was only ever judged in the pretty empty one.
        </p>
        <p class="home-intro-note">
          Rows are the shipped <code>SessionRow</code> in all three. Direction A mounts the shipped{" "}
          <code>Sidebar</code> itself. Nothing here is wired into the product.
        </p>
      </header>

      <section class="home-sheet">
        <h2>Contact sheet</h2>
        <p class="home-sheet-sub">
          The deciding state: one session running, one waiting for permission. Swipe sideways.
        </p>
        <ContactSheet stateId="waiting" scale={0.62} />
        <p class="home-sheet-sub">The same three with nothing waiting and two saved sessions.</p>
        <ContactSheet stateId="few" scale={0.62} />

        <table class="home-table">
          <thead>
            <tr><th>Direction</th><th>What it wins</th><th>What it costs</th></tr>
          </thead>
          <tbody>
            {HOME_DIRECTIONS.map((d) => (
              <tr key={d.id}>
                <td>
                  <b>{d.name}</b>
                  <span class="home-table-one">{d.one}</span>
                  <span class="home-table-principle">{d.principle}</span>
                </td>
                <td><ul>{d.wins.map((w) => <li key={w}>{w}</li>)}</ul></td>
                <td><ul>{d.costs.map((c) => <li key={c}>{c}</li>)}</ul></td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      {dirs.map(({ id, name, one, principle, wins, costs, Screen }) => (
        <section class="home-dir" key={id}>
          <h2>{name}</h2>
          <p class="home-dir-one">{one}</p>
          <p class="home-dir-principle"><b>Backed by</b> {principle}</p>
          <div class="home-dir-cols">
            <div><h3>Wins</h3><ul>{wins.map((w) => <li key={w}>{w}</li>)}</ul></div>
            <div><h3>Costs</h3><ul>{costs.map((c) => <li key={c}>{c}</li>)}</ul></div>
          </div>
          <div class="home-strip">
            {HOME_STATES.map((state) => (
              <Frame key={state.id} id={`${id}-${state.id}`} scale={0.92} caption={state.label} sub={state.note}>
                <Screen state={state} />
              </Frame>
            ))}
          </div>
        </section>
      ))}

      <section class="home-desk">
        <h2>Desktop</h2>
        <p>
          On the desktop the question does not arise: the Spine is already the list, permanently docked, and
          the pane beside it is where a conversation goes. That is exactly direction A, shipped years of
          pixels ago — which is the strongest argument for A on the phone and the reason B and C have to
          earn a second drawing of the same roster.
        </p>
        <p class="home-desk-note">
          The consequence for each direction at 1280px: <b>A</b> changes nothing (the phone becomes the
          desktop). <b>B</b> adds a page the desktop does not have and will not want. <b>C</b> is the only
          one with something to offer the desktop later — the waiting card has an obvious home in the empty
          pane, which today says nothing. The two frames below are that difference, with the SHIPPED Sidebar
          in the Spine of both. That the document has nothing to say about it is a hole: §2 writes five
          principles for the phone and never says what the desktop's first screen is.
        </p>
        <div class="home-strip">
          <DesktopFrame
            variant="blank"
            scale={0.48}
            caption="Today (A, at 1280)"
            sub="The Spine is the list; the empty pane says nothing."
          />
          <DesktopFrame
            variant="card"
            scale={0.48}
            caption="C's card in the empty pane"
            sub="The same object the phone raises, where the desktop has room for it."
          />
        </div>
      </section>
    </div>
  );
}
