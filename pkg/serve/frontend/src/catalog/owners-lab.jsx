import { useState } from "preact/hooks";
import { ChatHead } from "../layout/ChatHead/ChatHead.jsx";
import { MobileChrome } from "../layout/mobile/MobileChrome/MobileChrome.jsx";
import { Stream } from "../layout/Stream/Stream.jsx";
import { MobileStream } from "../layout/mobile/MobileConversationScreen/MobileStream.jsx";
import { Sidebar } from "../layout/Sidebar/Sidebar.jsx";
import { projectStream } from "../data/stream-model.js";
import { shortPath } from "../data/util/format.js";
// METODO §4: the lab has no private copy. These are the shipped components.
import { OwnerChip, OwnerPanel, OwnersView } from "../components/Owners/Owners.jsx";
import {
  CHILD_SESSION, LIST_SESSIONS, MOA_OWNER, OWNERS, WINERIM, WINERIM_ASKING, WINERIM_BOOK,
  WINERIM_SESSION,
} from "./owners-fixtures.js";
import "../layout/Stream/Stream.css";
import "../layout/mobile/SessionDrawer/SessionDrawer.css";
import "../layout/mobile/MobileConversationScreen/MobileConversationScreen.css";
import "../layout/mobile/MobileConversationScreen/MobileStream.css";
import "./owners-lab.css";

/* Owners — the project owner as a surface, both densities, every state.

   WHAT AN OWNER IS (backend, already shipped): one standing agent per
   codebase. It keeps the project's book (PROJECT.md + decisions/, people.md,
   areas/*.md under ~/.config/moa/codebases/<key>/book/), it starts and directs
   the ordinary sessions of that codebase, it receives their reports and it
   answers the questions the book already answers. Its conversation is an
   ordinary hidden session (kind:"owner"), so the transcript here is the
   SHIPPED one — nothing about an owner's transcript is new.

   THE DECISION THIS LAB NOW DRAWS (owner's call, after seeing the first pass)
   Owners is NOT a door beside the Inbox that replaces the list and has to be
   left. It is the sidebar's THIRD MODE, a sibling of Recent and By project
   (the `Session order` radiogroup, layout/Sidebar/Sidebar.jsx:41-42,304-320):
   you choose it and it stays chosen; to look at sessions you change mode.
   Consequences, all of them visible in the frames below:
     · the mode control has three options: Recent · By project · Owners
     · in Owners mode the column lists the owners, and nothing else changes:
       the head (wordmark, search, +, the control) and the foot (Inbox,
       version, settings) are the same objects they are in the other modes
     · there is no "‹ Owners" head and no way back, because there is nowhere
       to go back TO — a mode is not a page
     · selecting an owner marks its row the way a current session row is
       marked and opens its conversation in the pane
     · New owner is a page pushed inside the column, like New session
     · no owner appears in the Recent / By project lists: the backend hides an
       owner's conversation from GET /api/sessions

   HOW IT IS BUILT
   The frames are the lab's; everything inside them is production's. The
   Owners pieces now live in components/Owners and the sidebar's three-mode
   control is shipped, so this file has no private copy of either — the lab
   parameterises production and photographs it (METODO.md §4). */

const noop = () => {};

const PRESETS = [
  {
    id: "two",
    label: "Two owners",
    owners: OWNERS,
    note: "The ordinary state of the mode. The list is scanned by NAME — one owner per project, so the folder under it is the address, not the identity. The second line is the only number that matters: how many of its sessions have stopped until you answer.",
  },
  {
    id: "waiting",
    label: "Owner waiting on you",
    owners: [WINERIM_ASKING, MOA_OWNER],
    note: "Winerim's OWN conversation is asking (yellow dot beside the name), and two of its sessions have stopped (yellow line below). Two different things in two different places: the dot is the conversation, the line is the children.",
  },
  {
    id: "none",
    label: "No owners yet",
    owners: [],
    note: "Nobody has an owner. The mode is still a mode — head and foot unchanged — and the column's body is the one screen where the accent is spent on creating: with no list to look at, creating IS what it is for.",
  },
  {
    id: "loading",
    label: "Loading",
    owners: [],
    status: "loading",
    note: "First read after switching mode. Two ghosts at the row's rhythm — the list is two or three items long, so three ghosts would promise a list that never arrives.",
  },
  {
    id: "error",
    label: "Error",
    owners: [],
    status: "error",
    error: "GET /api/owners · 500 cannot resolve the moa config directory",
    note: "The request that failed, verbatim, and the reassurance that matters: owners and books are on disk. Red, because it is a failure — the yellow is reserved for what is waiting for an answer.",
  },
];

const VIEWS = [
  {
    id: "mode",
    label: "Owners mode",
    note: "The mode, with nothing chosen yet. Note what did NOT happen: the pane still holds the session you were reading, the head and the foot are the ones the other two modes have, and there is no way back because you did not go anywhere.",
  },
  {
    id: "selected",
    label: "An owner selected",
    note: "The row is marked the way a current session row is — raised plane, heavier name, no left bar (CRITERIO §1) — and its conversation is in the pane. The list stays in front of you: choosing another owner is one press away.",
  },
  {
    id: "new",
    label: "New owner",
    note: "A page pushed inside the column, exactly as New session is: folder first (the same /api/fs/complete explorer the palette's create step uses), then the name — which follows the folder until you type one. It is the one page here with a head, because a page IS something you leave.",
  },
  {
    id: "overview",
    label: "Owner · Overview",
    note: "The dossier, unchanged by this decision. The transcript is the SHIPPED one and the report batches in it are real event blocks (custom.source=\"report\" → projectStream → EventBlock): nobody typed them, so they must not wear the peach bar that means \"you said this\".",
  },
  { id: "book", label: "Owner · Book", note: "The same dossier, second tab. PROJECT.md is raised out of the list because it is the one file a child ever sees; opening it pushes a page inside the panel." },
  { id: "project", label: "Owner · Book › PROJECT.md", note: "Editable, 16px floor. The rest of the book is read-only in v1: it is the owner's own record, and a half-edited decision file is worse than none." },
  { id: "child", label: "In a child session", note: "Back in the Recent mode, where no owner row exists at all. The only thing a child gains: a quiet chip saying which owner it belongs to, and a door to it. No dot, no colour — it is provenance, not a state." },
];

const TRIAGE = [
  { id: "off", label: "Owner rows only", note: "The row alone. Shortest list, and the one that stays a list of owners rather than a second session list." },
  { id: "on", label: "…with what has stopped", note: "VARIANT, for the owner to judge: under each owner, the children that have STOPPED, at most three. It makes the mode usable for triage without opening a dossier; it costs the list its evenness, and it prints rows that also live in the Recent mode. Only what is waiting is ever shown — a running child here would be the session list drawn twice." },
];

/* ── The sidebar ───────────────────────────────────────────────────────────

   Production's own <Sidebar/>, in all three modes. The three-mode control, the
   Owners list, the head and the foot are the shipped ones: this lab no longer
   draws any of them, which is the whole point of the move (METODO.md §4). The
   owners it is given are fixtures; everything that arranges them is the app. */

function LabSidebar({
  mode,
  onMode,
  phone = false,
  preset,
  triage = false,
  activeOwnerId = null,
  activeSessionId = null,
  onOpenOwner,
}) {
  return (
    <Sidebar
      density={phone ? "phone" : "desktop"}
      version={{ current: "v0.37.4" }}
      active={LIST_SESSIONS}
      saved={[]}
      activeId={activeSessionId}
      mode={mode}
      onMode={onMode}
      owners={preset.owners}
      ownersHealth={{ status: preset.status || "ready", error: preset.error }}
      activeOwnerId={activeOwnerId}
      ownerTriage={triage}
      ownerDefaultDir="/home/ealeixandre/dev/winerim-backend"
      onOpenOwner={onOpenOwner}
      onOpenOwnerChild={noop}
      onCreateOwner={noop}
      onRetryOwners={noop}
      onSelectSession={noop}
      onNewSession={noop}
      onSearch={noop}
      onSettings={noop}
      inboxCount={2}
      inboxVisible
      onInbox={noop}
    />
  );
}

/* ── The two densities ─────────────────────────────────────────────────── */

function paneSession(view) {
  // What the pane holds. In the mode itself it is the session you were already
  // reading — switching mode does not move the transcript — and it only
  // becomes the owner's conversation once you choose an owner.
  if (view === "child" || view === "mode" || view === "new") return CHILD_SESSION;
  return WINERIM_SESSION;
}

function Desktop({ preset, view, triage, panel, mode, onMode, onOpenOwner }) {
  const session = paneSession(view);
  // Whose transcript is in the pane, and — separately — whether the frame is
  // the one about a child session. They are not the same: in the mode's own
  // frame the pane still holds the session you were reading, but you are not
  // "in a child", so it must not wear the owner chip.
  const inChild = view === "child";
  const childPane = session === CHILD_SESSION;
  const blocks = projectStream(session);
  const dossier = view === "overview" || view === "book" || view === "project";
  return (
    <div class="owl-desk-wrap">
      <div class="owl-density-label">Desktop · 1180 × 780</div>
      <div class="owl-desk">
        <div class="owl-desk-side">
          <LabSidebar
            mode={mode}
            onMode={onMode}
            preset={preset}
            triage={triage}
            activeOwnerId={view === "mode" || view === "new" ? null : WINERIM.id}
            activeSessionId={inChild ? CHILD_SESSION.id : null}
            onOpenOwner={onOpenOwner}
          />
        </div>
        <div class="owl-desk-main">
          <ChatHead
            title={childPane ? CHILD_SESSION.title : WINERIM.name}
            path={shortPath(session.cwd, 40)}
            onTitleClick={noop}
            /* headExtra is production's existing slot for extra head actions
               (ChatHead.jsx:56-58), so the chip needs no change to the head. */
            headExtra={inChild ? <OwnerChip name={WINERIM.name} onClick={noop} /> : null}
            onGridToggle={noop}
          />
          <div class="owl-desk-stream">
            <Stream session={session} blocks={blocks} onOpenSubagent={noop} />
          </div>
        </div>
        {dossier && (
          <div class="owl-desk-dossier">
            <OwnerPanel
              owner={WINERIM}
              book={WINERIM_BOOK}
              tab={panel.tab}
              onTab={panel.setTab}
              openPath={panel.path}
              onOpenFile={panel.setPath}
              onOpenChild={noop}
              onClose={noop}
            />
          </div>
        )}
      </div>
      <p class="owl-hint">
        {view === "mode"
          ? "The mode is chosen and it stays chosen. The head, the foot and the width are the ones the other two modes have; the pane never moved."
          : view === "new"
            ? "New owner is pushed inside the column, over the list, the way New session is. Its head exists because it is a page — the mode behind it has none."
            : inChild
              ? "In a child session the sidebar is back in Recent, where no owner row exists. The child gains the chip beside the head's actions, and nothing else."
              : "The chosen owner's row is marked and its conversation is in the pane. Its dossier is the shell's third zone, where this session's dossier already lives."}
      </p>
    </div>
  );
}

function Phone({ preset, view, triage, panel, mode, onMode, onOpenOwner }) {
  const session = paneSession(view);
  const inChild = view === "child";
  const blocks = projectStream(session);
  const dossier = view === "overview" || view === "book" || view === "project";
  // The drawer is open in every frame except the child one and the dossier's:
  // on the phone the sidebar IS the drawer, so a mode nobody can see is not a
  // frame worth photographing. Its chassis is the shipped SessionDrawer's
  // (sdrawer-veil / sdrawer, SessionDrawer.css), drawn open and at rest here
  // because the lab has no gesture and an animation photographs mid-flight.
  const drawerOpen = !inChild && !dossier;
  return (
    <div class="owl-phone-wrap">
      <div class="owl-density-label">Phone · 390 × 780</div>
      <div class="owl-phone">
        <div class="mconv owl-mconv">
          <MobileStream session={session} blocks={blocks} onOpenSubagent={noop} />
          <MobileChrome title={session === CHILD_SESSION ? CHILD_SESSION.title : WINERIM.name} open={drawerOpen} onToggle={noop} onNew={noop} />
          {/* The chip under the capsule row, not inside it: the three capsules
              are already ≡ / name / + and the title chip's own rule is that
              one control does one job (CRITERIO §3). */}
          {inChild && (
            <div class="owl-chip-row">
              <OwnerChip name={WINERIM.name} compact onClick={noop} />
            </div>
          )}
          {drawerOpen && (
            <div class="sdrawer-veil is-open">
              <div class="sdrawer is-open" role="dialog" aria-modal="true" aria-label="Sessions">
                <LabSidebar
                  phone
                  mode={mode}
                  onMode={onMode}
                  preset={preset}
                  triage={triage}
                  activeOwnerId={view === "mode" || view === "new" ? null : WINERIM.id}
                  activeSessionId={null}
                        onOpenOwner={onOpenOwner}
                />
              </div>
            </div>
          )}
          {dossier && (
            <>
              <div class="owl-scrim" />
              <div class="owl-msheet">
                <span class="owl-grab" aria-hidden="true" />
                <OwnerPanel
                  owner={WINERIM}
                  book={WINERIM_BOOK}
                  tab={panel.tab}
                  onTab={panel.setTab}
                  openPath={panel.path}
                  onOpenFile={panel.setPath}
                  onOpenChild={noop}
                  onClose={noop}
                  variant="sheet"
                />
              </div>
            </>
          )}
        </div>
      </div>
      <p class="owl-hint">
        {drawerOpen
          ? "The phone uses the same Sidebar inside the drawer, so the third mode arrives here for free — same control, same icons, same foot. Choosing an owner closes the drawer onto its conversation, exactly as choosing a session does."
          : inChild
            ? "The chip sits under the capsules, where it scrolls with the transcript. The capsule row keeps its three jobs."
            : "The dossier is the bottom sheet this session's dossier already uses (MobileSheet, bare), with the owner's two tabs inside it."}
      </p>
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
  const [presetId, setPresetId] = useState(() => params.get("owners") || "two");
  const [viewId, setViewId] = useState(() => params.get("owner") || "mode");
  const [triageId, setTriageId] = useState(() => params.get("triage") || "off");
  const [tab, setTab] = useState(() => (params.get("owner") === "book" || params.get("owner") === "project" ? "book" : "overview"));
  const [path, setPath] = useState(() => (params.get("owner") === "project" ? "PROJECT.md" : null));
  const preset = PRESETS.find((p) => p.id === presetId) || PRESETS[0];
  const shots = params.get("shots") === "1";

  const pickView = (id) => {
    setViewId(id);
    if (id === "overview") { setTab("overview"); setPath(null); }
    if (id === "book") { setTab("book"); setPath(null); }
    if (id === "project") { setTab("book"); setPath("PROJECT.md"); }
  };

  // The mode the sidebar is in. It follows the view — a child session is read
  // in Recent, everything else in Owners — and it is also a real control: the
  // three options in the frames switch it, which is the whole claim being
  // made ("you choose it and it stays chosen").
  const [modeOverride, setMode] = useState(null);
  const mode = modeOverride || (viewId === "child" ? "recent" : "owners");
  const onMode = (id) => { setMode(id); if (id !== "owners" && viewId !== "child") pickView("child"); if (id === "owners" && viewId === "child") pickView("mode"); };
  const onOpenOwner = () => pickView("selected");

  const panel = { tab, setTab, path, setPath };
  const triage = triageId === "on";
  const shared = { preset, view: viewId, triage, panel, mode, onMode, onOpenOwner };

  const stage = (
    <div class="owl-stage">
      <Phone {...shared} />
      <Desktop {...shared} />
    </div>
  );

  // ?shots=1 — the frames alone, for the capture script.
  if (shots) return <div class="owl is-shots">{stage}</div>;

  return (
    <div class="owl">
      <header class="owl-lab-head">
        <h1>Owners</h1>
        <p>
          One standing agent per project: it keeps the project's book, starts
          the sessions that work on it and reads what they report back. The
          sidebar's third mode — a sibling of Recent and By project — because
          choosing a project's owner is a way of looking at your work, not a
          place you visit and leave.
        </p>
      </header>

      <LabSeg label="Owners state" options={PRESETS} value={preset.id} onChange={(id) => { setPresetId(id); setMode("owners"); setViewId("mode"); }} />
      <LabSeg label="What to look at" options={VIEWS} value={viewId} onChange={(id) => { setMode(null); pickView(id); }} />
      <LabSeg label="Rows" options={TRIAGE} value={triageId} onChange={setTriageId} />

      {stage}

      <section class="owl-notes">
        <h2>Decisions, and why</h2>
        <dl>
          <dt>Owners is the sidebar's third MODE, not a door beside the Inbox.</dt>
          <dd>
            Decided by the owner after seeing the first pass, which made it a door: a surface that took over the
            column, wore a "‹ Owners" head and had to be left. Recent, By project and Owners are three ways of
            looking at the same work, and a way of looking is chosen and kept — it is not somewhere you go. So the
            mode control (<span class="owl-data">layout/Sidebar/Sidebar.jsx:41-42,304-320</span>) gets a third
            option and everything else in the column stays exactly where it was. The phone gets it for free: the
            drawer mounts the same <span class="owl-data">Sidebar</span>.
          </dd>
          <dt>The head and the foot belong to the column, not to the mode.</dt>
          <dd>
            Wordmark, search, "+" and the mode control above; Inbox, version and settings below — identical in all
            three modes. The first pass lost the Inbox while Owners was open, which is not a bug to fix so much as
            what the door shape led to: two doors competing for one foot. A mode cannot take the foot away.
          </dd>
          <dt>The chosen owner is marked like a current session, and nothing else moves.</dt>
          <dd>
            Raised plane and a heavier name (<span class="owl-data">SessionRow.css .zl-row.is-current</span>), never
            a left bar: that gesture means "you said this" (CRITERIO §1). The list stays on screen with the owner's
            conversation in the pane, so the second owner is one press away rather than a back-and-forward.
          </dd>
          <dt>No owner appears in the Recent or By project lists.</dt>
          <dd>
            The backend already hides an owner's conversation from <span class="owl-data">GET /api/sessions</span>,
            and the fixtures now say the same thing: the roster in those two modes is the children only. An owner
            listed among the sessions it is responsible for would be one of its own rows.
          </dd>
          <dt>Showing what has stopped under each owner is a VARIANT, not the design.</dt>
          <dd>
            The "Rows" control above switches it. It buys triage without opening a dossier — the two stopped
            children are the reason you looked — and it costs the list its evenness and prints rows that also live
            in the other two modes. Only stopped children are ever offered, at most three
            (<span class="owl-data">owners-model.js waitingChildren</span>): a running child here would be the
            session list drawn twice. The owner's call.
          </dd>
          <dt>New owner is a page pushed inside the column.</dt>
          <dd>
            Like New session, and for the same reason: it is a form, it is temporary, and it has somewhere to
            return to — the mode behind it. It is therefore the only thing in this design with a head and a back
            arrow, which is exactly what tells you it is a page and the mode is not.
          </dd>
          <dt>The row is scanned by name; the number that pulls the eye is what has stopped.</dt>
          <dd>
            One owner per codebase, so the name identifies it and the folder is the address. The second line counts
            live children and then, only when there is something, "N waiting on you" in yellow — the reason line's
            tone rule from the session row (<span class="owl-data">SessionRow.css .zl-row-brief.tone-*</span>).
            Unread results get mauve, which is what mauve already means.
          </dd>
          <dt>Two things ask for you, in two places: the dot and the line.</dt>
          <dd>
            The dot beside the name is the OWNER's own conversation (its <span class="owl-data">session_state</span>);
            the line below is its children. They are genuinely different — the owner can be idle while two of its
            sessions are blocked — and merging them into one badge would lose which.
          </dd>
          <dt>Overview and Book are TABS of the owner's dossier, in the shell's third zone.</dt>
          <dd>
            Unchanged by this decision. An owner has exactly two things to show — its children and its book — so a
            root page of two rows that each push would be a menu whose only job is to be left. The zone itself is
            not new: it is where this session's dossier already lives (<span class="owl-data">DesktopDossier</span>
            docked, <span class="owl-data">MobileSheet</span> on the phone), which is what makes the owner read as
            a peer of a session rather than a second application.
          </dd>
          <dt>PROJECT.md is editable; the rest of the book is not, in v1.</dt>
          <dd>
            It is the only file injected into a child's prompt (<span class="owl-data">pkg/owner/owner.go:44-50</span>),
            so it is the one whose wording you may need to fix yourself. The rest is the owner's own record: a
            half-edited decision file is worse than none.
          </dd>
          <dt>The child gets a chip and nothing else.</dt>
          <dd>
            Provenance, not state: no dot, no identity colour, no count. On the desktop it needs no change to
            production — <span class="owl-data">ChatHead</span> already takes a <span class="owl-data">headExtra</span>
            slot. On the phone it sits under the capsule row rather than inside it.
          </dd>
          <dt>Reports in the owner's transcript are event blocks, not messages.</dt>
          <dd>
            Already true in production (<span class="owl-data">data/stream-model.js:453-467</span>): nobody typed a
            report, so it must not wear the left bar that means "you said this". The transcript in these frames is
            the shipped one, fed by the shipped projection.
          </dd>
        </dl>
      </section>

      <section class="owl-notes">
        <h2>What production would have to change</h2>
        <dl>
          <dt>Sidebar: the mode control becomes three-way.</dt>
          <dd>
            <span class="owl-data">ORDERS</span> grows one entry and the boolean <span class="owl-data">groupByProject</span>
            becomes a mode string; the travelling pill needs a third stop (<span class="owl-data">.zl-view::before</span>,
            28px per step on the desktop and 44 on the phone). Measured in these frames, the head still fits: 264 of 272
            points docked, 328 of 340 in the drawer. Tight enough that a fourth mode would not fit — worth knowing before
            anyone proposes one.
          </dd>
          <dt>Backend that the surface still needs.</dt>
          <dd>
            The children of an owner grouped by state, and the book over HTTP (<span class="owl-data">GET/PUT</span>);
            today the book is only on disk and <span class="owl-data">GET /api/owners/&#123;id&#125;</span> does not
            group. Both are drawn here from fixtures, and both are named in the report rather than pretended to exist.
          </dd>
        </dl>
      </section>
    </div>
  );
}
