import { focusedSessionId } from "../../data/selectors.js";
import { sessionDisplayDotState, sessionTitle, shortPath } from "../../data/util/format.js";
import { attentionKind, ordinarySessions } from "../../data/util/project-sessions.js";
import { ownerRows } from "../../data/owners-model.js";
import { ownersHealth, ownersSlice } from "../../data/owners.js";
import { allTileIds, findTile } from "../../data/tileTree.js";
import { inboxCards, inboxHealth, inboxHealthSig, inboxPendingCount, inboxSig } from "../../data/events.js"; // wake-on-event

function relAge(updated) {
  if (!updated) return "";
  const diff = Date.now() - updated;
  const min = Math.floor(diff / 60000);
  if (min < 1) return "now";
  if (min < 60) return `${min}m`;
  const h = Math.floor(min / 60);
  if (h < 24) return `${h}h`;
  const d = Math.floor(h / 24);
  return `${d}d`;
}

/* ── The second line of a session row ─────────────────────────────────────
   It says WHY the session wants you, not what the conversation was about.
   The list is a queue of things to do; the summary of the talk is inside the
   session, one click away, and it never told you whether to open it.

   Nothing here is a new datum: the reason is the display state that already
   colours the dot (sessionDisplayDotState) narrowed through attentionKind,
   which is the same predicate the Needs attention group partitions with — so
   the group and the sentence can never disagree. The elapsed minutes come
   from runStartedAtMs, already in the store for the live bar's timer.

   A session with no reason (idle, saved) returns null and the row falls back
   to its path: two lines is the budget, and the more useful datum wins. */
const REASON_TONE = { permission: "yellow", error: "red", unseen: "mauve" };

function runningFor(sess, now) {
  const started = sess.runStartedAtMs || 0;
  if (!started) return "";
  const min = Math.floor(Math.max(0, now - started) / 60000);
  if (min < 1) return "";
  if (min < 60) return `${min}m`;
  return `${Math.floor(min / 60)}h`;
}

export function sessionRowReason(sess, now = Date.now()) {
  if (!sess) return null;
  const kind = attentionKind(sess);
  if (kind === "permission") return { text: "Needs your answer", tone: REASON_TONE.permission };
  if (kind === "error") return { text: "Stopped with an error", tone: REASON_TONE.error };
  if (kind === "unseen") return { text: "Answered · not read yet", tone: REASON_TONE.unseen };
  if (sessionDisplayDotState(sess) === "running") {
    const age = runningFor(sess, now);
    return { text: age ? `Running · ${age}` : "Running", tone: "neutral" };
  }
  return null;
}

export function sessionRowBrief(sess) {
  return sessionRowReason(sess)?.text || "";
}

function toSpineRow(s, extra = {}) {
  const reason = sessionRowReason(s);
  return {
    id: s.id,
    title: sessionTitle(s),
    state: sessionDisplayDotState(s),
    unseen: !!s.unseen,
    when: relAge(s.updated),
    brief: reason?.text || "",
    briefTone: reason?.tone || "",
    path: reason ? "" : (shortPath(s.cwd) || s.cwd || ""),
    origin: s.origin || undefined,
    cwd: s.cwd || "",
    updated: s.updated || 0,
    ...extra,
  };
}

// spineSessions — open vs saved, newest first. Optional paneOf (session id →
// "P1") is the grid's badge; conversation view omits it.
export function spineSessions(sessions, paneOf) {
  // The owners' own conversations are in the roster but never in this list:
  // they are reached from the sidebar's Owners mode.
  const all = ordinarySessions(Object.values(sessions || {}));
  const active = all
    .filter((s) => s.state !== "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0))
    .map((s) => toSpineRow(s, paneOf?.get(s.id) ? { pane: paneOf.get(s.id) } : {}));
  const saved = all
    .filter((s) => s.state === "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0))
    .map((s) => toSpineRow(s, { saved: true }));
  return { active, saved };
}

export function paneBadges(tileTree) {
  const paneOf = new Map();
  if (!tileTree) return paneOf;
  for (const [i, tileId] of allTileIds(tileTree).entries()) {
    const tile = findTile(tileTree, tileId);
    if (tile && tile.sessionId) paneOf.set(tile.sessionId, `P${i + 1}`);
  }
  return paneOf;
}

// activeOwnerIdOf — the owner whose own conversation is the one on screen.
// Read from the session rather than stored: the pane is the single source of
// "where you are", and a second field could disagree with it.
export function activeOwnerIdOf(state) {
  const id = state?.isMobile ? state.activeSession : focusedSessionId(state);
  const sess = id ? state.sessions[id] : null;
  if (!sess || (sess.kind || "") !== "owner") return null;
  const own = (ownersSlice(state).list || []).find((o) => o.session_id === sess.id);
  return own ? own.id : null;
}

export function focusedTileSessionId(state) {
  const t = findTile(state.tileTree, state.focusedTile);
  return t ? t.sessionId : null;
}

function spineRowSig(row) {
  return [
    row.id, row.title, row.state, row.unseen ? 1 : 0, row.when, row.brief,
    row.path, row.origin || "", row.pane || "", row.saved ? 1 : 0,
  ].join("\0");
}

function spineListSig(list) {
  return (list || []).map(spineRowSig).join("\n");
}

function ownersSig(list) {
  return (list || []).map((o) => [
    o.id, o.name, o.session_state || "", (o.children || []).map((c) => c.id + c.state + (c.unseen ? 1 : 0)).join(","),
  ].join("\0")).join("\n");
}

function desktopChromeEqual(a, b) {
  return a.activeId === b.activeId
    && a.sidebarMode === b.sidebarMode
    && a.activeOwnerId === b.activeOwnerId
    && a.ownersHealth?.status === b.ownersHealth?.status
    && a.ownersHealth?.error === b.ownersHealth?.error
    && a.ownersHealth?.retrying === b.ownersHealth?.retrying
    && ownersSig(a.owners) === ownersSig(b.owners)
    && a.groupByProject === b.groupByProject
    && a.drawerCollapsed === b.drawerCollapsed
    && a.soundEnabled === b.soundEnabled
    && a.inboxOpen === b.inboxOpen // wake-on-event
    && a.inboxPending === b.inboxPending // wake-on-event
    && inboxSig(a.inbox) === inboxSig(b.inbox) // wake-on-event
    && inboxHealthSig(a.inboxHealth) === inboxHealthSig(b.inboxHealth) // wake-on-event
    && spineListSig(a.active) === spineListSig(b.active)
    && spineListSig(a.saved) === spineListSig(b.saved);
}

// selectDesktopChrome — roster the sidebar actually paints. Streaming text,
// tokens and messages are not on a row, so a token on any session returns the
// previous object (Object.is) and DesktopShell does not re-render.
export function selectDesktopChrome(state) {
  const inGrid = state.view === "grid";
  const paneOf = inGrid ? paneBadges(state.tileTree) : undefined;
  const { active, saved } = spineSessions(state.sessions, paneOf);
  const inbox = inboxCards(state.sessions, state.events); // wake-on-event
  const next = {
    active,
    saved,
    inbox,
    // Whether that list can be believed. It travels WITH the list: a chrome
    // holding the rows but not their truthfulness is exactly what let the
    // surface say "Nothing waiting." after a load that never succeeded.
    inboxHealth: inboxHealth(state), // wake-on-event
    inboxOpen: !!state.inboxOpen, // wake-on-event
    // The count the foot's door shows. It travels with the list for the same
    // reason the health does: a chrome that held the rows but not their count
    // would make the door and the list disagree.
    inboxPending: inboxPendingCount(inbox), // wake-on-event
    activeId: inGrid ? focusedTileSessionId(state) : focusedSessionId(state),
    // The owners, joined with their children out of the roster this same
    // snapshot holds: one projection, so the dossier and the list can never
    // disagree about which session is waiting.
    owners: ownerRows(ownersSlice(state).list, state.sessions),
    ownersHealth: ownersHealth(state),
    // Which owner's conversation is in the pane, so its row is marked.
    activeOwnerId: activeOwnerIdOf(state),
    sidebarMode: state.sidebarMode || (state.groupByProject ? "project" : "recent"),
    groupByProject: !!state.groupByProject,
    // The folder accordion is one persisted preference, not one per surface:
    // collapsing a folder on the phone and finding it open on the desktop was
    // the same list disagreeing with itself.
    drawerCollapsed: state.drawerCollapsed,
    soundEnabled: !!state.soundEnabled,
  };
  const prev = selectDesktopChrome._prev;
  if (prev && desktopChromeEqual(prev, next)) return prev;
  selectDesktopChrome._prev = next;
  return next;
}

export function __resetDesktopChromeForTests() {
  selectDesktopChrome._prev = null;
}
