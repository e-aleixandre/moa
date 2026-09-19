import { useState } from "preact/hooks";
import { ChatHead } from "../layout/ChatHead/ChatHead.jsx";
import { MobileChrome } from "../layout/mobile/MobileChrome/MobileChrome.jsx";
import { Stream } from "../layout/Stream/Stream.jsx";
import { MobileStream } from "../layout/mobile/MobileConversationScreen/MobileStream.jsx";
import { projectStream } from "../data/stream-model.js";
import { shortPath } from "../data/util/format.js";
import { Field } from "../primitives/Field/Field.jsx";
import { Button } from "../primitives/Button/Button.jsx";
/* The lab imports the SHIPPED pieces, not copies of them: the avatar, the
   owner row and its section heading are production's own files now, so what
   is photographed here is the product rather than a translation of it
   (tmp/redesign/fidelity/METODO.md). What stays local is only the SCENE — the
   frames, the fixtures and the sidebar host below, which exists because the
   real Sidebar is wired to the store and this page has none. */
import {
  OwnerAvatar, AVATAR_COLORS, AVATAR_SHAPES, AVATAR_EYE_STATES, defaultAvatar,
} from "../components/Owners/OwnerAvatar.jsx";
import { OwnerRow } from "../components/Owners/OwnerRow.jsx";
import { NewOwnerDialog } from "../components/Owners/NewOwnerDialog.jsx";
import { OwnersSidebar, useAvatarChoice } from "./owners-sidebar.jsx";
import {
  CHILD_SESSION, MOA_OWNER, OWNERS, PROMOTED, SAVED, SESSIONS, STATE_ROWS, WINERIM,
  WINERIM_ASKS_WAITING, WINERIM_WEB,
} from "./owners3-fixtures.js";
import "../layout/Stream/Stream.css";
import "../layout/mobile/SessionDrawer/SessionDrawer.css";
import "../layout/mobile/MobileConversationScreen/MobileConversationScreen.css";
import "../layout/mobile/MobileConversationScreen/MobileStream.css";
import "./owners-lab.css";

/* Owners — ITERATION 3. The owner as a SECTION of the sidebar, not a mode.

   WHAT THE OWNER DECIDED, and what this lab therefore draws:

   1. The segmented is back to TWO positions. It says how the sessions are
      ORDERED (Recent · By project); it never said "which list", and making
      Owners its third stop was asking one control to do two jobs.

   2. In Recent, OWNERS is a section above everything:
        OWNERS · NEEDS ATTENTION · ACTIVE · SAVED
      Owners, Active and Saved collapse with the project group's exact
      mechanics (chevron, count always visible, persisted). Needs attention
      does not collapse: it is a PROMOTION, empty when nothing is wrong, and a
      collapsed alarm is an alarm you have chosen not to hear.

   3. In By project, the owner is the FIRST ROW of its group and there is no
      Owners section at all. A row printed in a section AND inside its folder
      is the same row twice — the mistake the first iteration made with triage.

   4. An owner NEVER rises into Needs attention. Its state is painted on its
      own row: a dot, a coloured lead clause, and — separately — how many of
      its children have stopped. Two facts about two different conversations;
      one badge would lose which one you have to go and unblock.

   5. The identity mark is an AVATAR: shape × colour (48 combinations), with
      eyes that change only with state. Identity colour ≠ state colour
      (CRITERIO §1), so the palette has no amber, red or green in it. The
      reason it exists is in the fixtures: "Winerim" and "Winerim Web" are two
      real projects with the same two initials, and a monogram calls them both
      "Wi".

   6. The triage rows under an owner are GONE. Needs attention is directly
      below the Owners section and says the same thing better.

   The frames are the lab's; the head, the foot, the session rows and the
   transcript inside them are production's. The pieces that production does not
   have yet (OwnerRow, the collapsible section head, OwnerAvatar, the identity
   picker, the chip) are candidates in src/catalog, written with the classes
   and the sheets they would carry into src/components unchanged. */

const noop = () => {};

const PRESETS = [
  {
    id: "recent",
    label: "Recent",
    mode: "recent",
    owners: OWNERS,
    note: "The ordinary column. OWNERS above everything — three rows that never grow, and what everything below belongs to — then the promotion, then the work, then the archive. Each owner's line is its own state first and its stopped children second: Winerim is idle with two of its sessions waiting on you, Winerim Web is reading reports, moa has two reports nobody has read.",
  },
  {
    id: "collapsed-active",
    label: "Recent, Active collapsed",
    mode: "recent",
    owners: OWNERS,
    sessions: PROMOTED,
    collapsed: { active: true },
    note: "Active folded away, and a third session promoted into Needs attention so the column is not just a list of chevrons. This is the state the collapse is FOR: nine running sessions you are not looking at right now, three rows you are. The count stays on the folded heading, because a section you must open to size is a section you open twice.",
  },
  {
    id: "project",
    label: "By project",
    mode: "project",
    owners: OWNERS,
    note: "The same owners, no Owners section. Each one is the first row of its group, under the heading that just named the project — so it reads as \"this folder, and the agent that keeps it\". The owner row is still visibly not a session: avatar, heavier name, the `owner` tag, no age. A project without an owner is exactly what it is today.",
  },
  {
    id: "asks",
    label: "Owner asks you",
    mode: "recent",
    owners: [WINERIM_ASKS_WAITING, WINERIM_WEB, MOA_OWNER],
    note: "Winerim asks something of its own accord AND two of its children have stopped. The question is quoted on the row — \"Asks you: ¿desplegamos hoy?\" — because a row that only says \"asks you\" is a notification, and you would have to open it to learn whether it can wait. The owner stays where it is: it does not climb into Needs attention.",
  },
  {
    id: "owners-collapsed",
    label: "Owners collapsed",
    mode: "recent",
    owners: [WINERIM_ASKS_WAITING, WINERIM_WEB, MOA_OWNER],
    collapsed: { owners: true },
    note: "Folded, the heading keeps its count and gains ONE mark: the most urgent thing it is hiding. Amber here, because Winerim is asking. One dot and no more — a collapsed section that summarised its contents would be the list you asked not to see, drawn smaller.",
  },
  {
    id: "new",
    label: "New owner",
    mode: "recent",
    owners: OWNERS,
    page: "new",
    note: "PRODUCTION's own dialog: a centred modal on the desktop, a bottom sheet on a phone — no longer a page that takes over the column. Above, the face and the two rows that change it: six shapes and eight colours, every swatch 44px in both densities, the selection a ring rather than a fill. Below the name, the model is ONE ROW that opens the product's ModelSelector, thinking included. Both densities are drawn at once here, so the modal and the sheet overlap.",
  },
  {
    id: "gallery",
    label: "Avatar gallery",
    mode: "recent",
    owners: OWNERS,
    page: "gallery",
    note: "All 48 identities, the four eye states, and the eight things a row can say. The eyes are the ONLY thing state moves: idle looks at you, working holds a glance aside (static — a drifting eye would be an animation in a column you look at for hours), asks raises a brow, saved shuts them. Nothing here blinks.",
  },
];

/* ── New owner ────────────────────────────────────────────────────────────
   The lab draws PRODUCTION's dialog now, not a mock of it: `NewOwnerDialog`
   is the centred modal on the desktop and the bottom sheet on a phone, with
   the real form inside — identity picker, folder explorer, name, and the model
   row that opens the product's own ModelSelector. The lab's own cut-down copy
   is gone: it existed when the form was a page of the column, and a second
   drawing of a shipped surface is exactly the drift the method forbids
   (tmp/redesign/fidelity/METODO.md).

   The fetches inside it are answered by the catalogue backend
   (catalog/catalog-backend.js: /api/models, /api/capabilities,
   /api/fs/complete), so what is photographed is the real thing with fixture
   data, and Create is the real request against that stub. */
function NewOwnerSurface({ phone = false }) {
  return (
    <NewOwnerDialog
      open
      phone={phone}
      defaultDir="/home/ealeixandre/dev/winerim-web"
      onCreate={async () => {}}
      onClose={noop}
    />
  );
}

/* ── The avatar gallery ─────────────────────────────────────────────────── */

function Gallery({ phone = false }) {
  const size = phone ? 28 : 32;
  return (
    <div class="ow-gal">
      <section class="ow-gal-sec">
        <h3 class="ow-gal-h">Shape × colour — 48 identities, none of them a state</h3>
        <div class="ow-gal-grid">
          {AVATAR_SHAPES.map((shape) => (
            <div class="ow-gal-line" key={shape}>
              <span class="ow-gal-k">{shape}</span>
              {AVATAR_COLORS.map((c) => (
                <OwnerAvatar key={c.id} shape={shape} color={c.id} state="idle" size={size} />
              ))}
            </div>
          ))}
          <div class="ow-gal-line is-key">
            <span class="ow-gal-k" />
            {AVATAR_COLORS.map((c) => <span class="ow-gal-cname" key={c.id}>{c.id}</span>)}
          </div>
        </div>
      </section>

      <section class="ow-gal-sec">
        <h3 class="ow-gal-h">Shape × state — the eyes are the only thing that moves with state</h3>
        <div class="ow-gal-grid">
          {AVATAR_SHAPES.map((shape, i) => (
            <div class="ow-gal-line" key={shape}>
              <span class="ow-gal-k">{shape}</span>
              {AVATAR_EYE_STATES.map((state) => (
                <OwnerAvatar key={state} shape={shape} color={AVATAR_COLORS[i].id} state={state} size={size} />
              ))}
            </div>
          ))}
          <div class="ow-gal-line is-key">
            <span class="ow-gal-k" />
            {AVATAR_EYE_STATES.map((s) => <span class="ow-gal-cname" key={s}>{s}</span>)}
          </div>
        </div>
      </section>

      <section class="ow-gal-sec">
        <h3 class="ow-gal-h">What an owner row can say</h3>
        <div class="ow-gal-rows">
          {STATE_ROWS.map(({ key, owner, note }) => (
            <div class="ow-gal-row" key={key}>
              <div class="ow-gal-rowbox">
                <OwnerRow owner={owner} onOpen={noop} />
              </div>
              <p class="ow-gal-note">{note}</p>
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}

/* ── The two densities ───────────────────────────────────────────────── */

function sidebarProps(preset, state) {
  return {
    mode: state.mode,
    onMode: state.setMode,
    owners: preset.owners,
    sessions: preset.sessions || SESSIONS,
    saved: SAVED,
    activeId: CHILD_SESSION.id,
    collapsed: state.collapsed,
    onToggleSection: state.toggleSection,
    collapsedProjects: state.collapsedProjects,
    onToggleProject: state.toggleProject,
    onOpenOwner: noop,
    onSelectSession: noop,
    onNewSession: noop,
    onSearch: noop,
    onSettings: noop,
    onInbox: noop,
  };
}

function Desktop({ preset, state }) {
  const blocks = projectStream(CHILD_SESSION);
  const page = preset.page;
  return (
    <div class="owl-desk-wrap">
      <div class="owl-density-label">Desktop · 1180 × 780</div>
      <div class="owl-desk">
        <div class="owl-desk-side">
          <OwnersSidebar density="desktop" {...sidebarProps(preset, state)} />
        </div>
        {/* The modal PORTALS to <body>, as it does in the app, so it centres on
            the window rather than inside this frame. That is the surface being
            judged: a centred modal is centred on the screen. */}
        {page === "new" ? <NewOwnerSurface /> : null}
        <div class="owl-desk-main">
          {page === "gallery" ? (
            <div class="owl-gal-pane"><Gallery /></div>
          ) : (
            <>
              <ChatHead
                title={CHILD_SESSION.title}
                path={shortPath(CHILD_SESSION.cwd, 40)}
                onTitleClick={noop}
                onGridToggle={noop}
              />
              <div class="owl-desk-stream">
                <Stream session={CHILD_SESSION} blocks={blocks} onOpenSubagent={noop} />
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function Phone({ preset, state }) {
  const blocks = projectStream(CHILD_SESSION);
  const page = preset.page;
  if (page === "gallery") {
    return (
      <div class="owl-phone-wrap">
        <div class="owl-density-label">Phone · 390 × 780</div>
        <div class="owl-phone"><div class="owl-gal-pane is-phone"><Gallery phone /></div></div>
      </div>
    );
  }
  return (
    <div class="owl-phone-wrap">
      <div class="owl-density-label">Phone · 390 × 780 · drawer 300</div>
      <div class="owl-phone">
        <div class="mconv owl-mconv">
          <MobileStream session={CHILD_SESSION} blocks={blocks} onOpenSubagent={noop} />
          <MobileChrome title={CHILD_SESSION.title} open onToggle={noop} onNew={noop} />
          <div class="sdrawer-veil is-open">
            <div class="sdrawer is-open" role="dialog" aria-modal="true" aria-label="Sessions">
              <OwnersSidebar density="phone" {...sidebarProps(preset, state)} />
            </div>
          </div>
          {page === "new" ? <NewOwnerSurface phone /> : null}
        </div>
      </div>
    </div>
  );
}

function LabSeg({ label, options, value, onChange }) {
  const cur = options.find((s) => s.id === value);
  return (
    <div class="owl-lab-ctl">
      <div class="owl-lab-seg" role="radiogroup" aria-label={label}>
        {options.map((s) => (
          <button
            type="button"
            role="radio"
            aria-checked={s.id === value}
            class={`owl-lab-opt${s.id === value ? " is-on" : ""}`}
            onClick={() => onChange(s.id)}
            key={s.id}
          >
            {s.label}
          </button>
        ))}
      </div>
      {cur?.note && <p class="owl-lab-note">{cur.note}</p>}
    </div>
  );
}

export function OwnersLab() {
  const params = new URLSearchParams(typeof location === "undefined" ? "" : location.search);
  const [presetId, setPresetId] = useState(() => params.get("preset") || "recent");
  const preset = PRESETS.find((p) => p.id === presetId) || PRESETS[0];

  const [modeOverride, setMode] = useState(null);
  const [collapsedOverride, setCollapsed] = useState(null);
  const [collapsedProjects, setCollapsedProjects] = useState({});
  const mode = modeOverride || preset.mode;
  const collapsed = collapsedOverride || preset.collapsed || {};
  const state = {
    mode,
    setMode,
    collapsed,
    toggleSection: (key) => setCollapsed({ ...collapsed, [key]: !collapsed[key] }),
    collapsedProjects,
    toggleProject: (key, open) => setCollapsedProjects((c) => ({ ...c, [key]: open })),
  };

  const stage = (
    <div class="owl-stage">
      <Phone preset={preset} state={state} />
      <Desktop preset={preset} state={state} />
    </div>
  );

  if (params.get("shots") === "1") return <div class="owl is-shots">{stage}</div>;

  return (
    <div class="owl">
      <header class="owl-lab-head">
        <h1>Owners — iteration 3</h1>
        <p>
          One standing agent per project, in the column where you already look
          for your work: a SECTION above the sessions in Recent, and the first
          row of its folder in By project. Not a mode — the segmented says how
          the sessions are ordered, and an owner is not an order.
        </p>
      </header>

      <LabSeg
        label="What to look at"
        options={PRESETS}
        value={preset.id}
        onChange={(id) => { setPresetId(id); setMode(null); setCollapsed(null); setCollapsedProjects({}); }}
      />

      {stage}

      <section class="owl-notes">
        <h2>Decisions, and why</h2>
        <dl>
          <dt>The segmented has two positions again; Owners is a section.</dt>
          <dd>
            Recent and By project are two ORDERS of the same sessions. Owners was never an order, so as a third
            stop it made one control answer two questions ("how is this sorted" and "what is this a list of"),
            and you could not see your owners and your work at the same time. As a section it costs three rows at
            the top of Recent and gives the column back.
          </dd>
          <dt>Owners, Active and Saved collapse. Needs attention does not.</dt>
          <dd>
            The collapsing is the project group's, down to the chevron and the rotation
            (<span class="owl-data">Sidebar.css .zl-group.is-proj / .zl-proj-chev</span>): one gesture in the
            column, not two that look alike. The count stays on a folded heading, or you would have to open a
            section to learn whether opening it was worth it. Needs attention is exempt because it is a
            PROMOTION rather than a list you keep — it is empty when nothing is wrong, and a collapsed alarm is
            an alarm you have chosen not to hear.
          </dd>
          <dt>An owner never rises into Needs attention.</dt>
          <dd>
            A session leaves Active for Needs attention and comes back when it is answered; that works because a
            session is a piece of work that ends. An owner is standing: it is in the column permanently, and a
            permanent row that moves between sections is a row you have to find again every time it changes.
            So its state is painted where it lives, and <span class="owl-data">kind:"owner"</span> stays out of
            the attention split.
          </dd>
          <dt>Two clauses on the owner's line, because they are two conversations.</dt>
          <dd>
            The lead is the OWNER's own state and takes its colour — amber asking, blue working, mauve unread,
            grey idle. "N waiting on you" is a second clause and is ALWAYS amber, because it is about its
            children and it is the number that stops work. A working owner with two blocked sessions therefore
            reads as one blue fact and one amber fact, not as one line in a compromise colour.
          </dd>
          <dt>The question is quoted on the row.</dt>
          <dd>
            "Asks you: ¿desplegamos hoy?" and not "Asks you". The difference is whether you can decide from the
            list: the second forces you to open the conversation to find out if it can wait, which is the whole
            cost the section was supposed to save.
          </dd>
          <dt>The identity mark is shape × colour; only the eyes carry state.</dt>
          <dd>
            Six shapes × eight colours = 48 identities, hashed deterministically from
            <span class="owl-data">codebase_key</span> so an owner looks like itself before anyone chooses
            anything, and so renaming it does not change its face. The palette is derived from tokens
            (peach, mauve, sky, teal, lavender, flamingo) and muted: it contains no amber, red or green, because
            those three are the product's state dots and an avatar that borrowed one would say "error" by being
            pink (CRITERIO §1). The fixtures make the case for the mark existing: "Winerim" and "Winerim Web"
            are two real projects with the same two initials.
          </dd>
          <dt>Four eye states, and nothing animates.</dt>
          <dd>
            idle open, working looking aside and HELD, asks with a raised brow, saved shut. A blink or a drift
            would be permanent motion in a column that is looked at for hours (CRITERIO §5), so there is no
            keyframe here at all — which also means <span class="owl-data">prefers-reduced-motion</span> has
            nothing to switch off. <span class="owl-data">unread</span> is deliberately not a fifth face: an
            unread report is news sitting in the row's line, not something the owner is doing.
          </dd>
          <dt>In By project the owner is a row, not a section.</dt>
          <dd>
            The heading has just named the project, so a second heading for its owner would be a section of one.
            A hairline under the row separates it from the sessions instead of a gap, which would have made it
            look like part of the heading rather than the first member of the group.
          </dd>
          <dt>The triage rows under an owner are gone.</dt>
          <dd>
            They printed rows that also lived in the list below. With Needs attention directly under the Owners
            section, the same sessions are already one glance away, in their own section, with their own titles.
          </dd>
          <dt>The chip carries the avatar at 20px.</dt>
          <dd>
            Same mark as the list, so the chip points at something you have seen rather than naming it. It stays
            provenance and not state in the sense that matters — it takes no dot and no count — but its eyes are
            the owner's, which is useful precisely where you are when the owner cannot reach you.
          </dd>
        </dl>
      </section>

      <section class="owl-notes">
        <h2>What production would have to change</h2>
        <dl>
          <dt>Sidebar: sections instead of a third mode.</dt>
          <dd>
            <span class="owl-data">ORDERS</span> drops back to two and the travelling pill loses its third stop
            (<span class="owl-data">.zl-view.is-owners::before</span>). The three list sections become
            collapsible headings with persisted state, beside the existing
            <span class="owl-data">collapsedProjects</span>; in By project the group renders its owner first.
            <span class="owl-data">partitionByAttention</span> keeps owners out by construction, since they are
            not in the session roster at all.
          </dd>
          <dt>Backend: <span class="owl-data">avatar:&#123;shape,color&#125;</span> in owner.json, and the row's state.</dt>
          <dd>
            The avatar is additive and optional — absent, the deterministic default from
            <span class="owl-data">codebase_key</span> is what every client draws, so old and new agree. The
            row also needs what today's <span class="owl-data">GET /api/owners</span> does not send: the owner's
            own pending question, its unread report count, and how many of its children have stopped. Both are
            drawn here from fixtures rather than pretended to exist.
          </dd>
        </dl>
      </section>
    </div>
  );
}
