import { focusedSessionId } from "../../../data/selectors.js";
import { shortPath, sessionDisplayDotState, sessionTitle } from "../../../data/util/format.js";
import { sessionRowReason } from "../../Sidebar/sessions.js";
import { aggregateAttention, newResultSessions } from "./attention-model.js";
import { inboxCards, inboxHealth, inboxHealthSig, inboxSig } from "../../../data/events.js"; // wake-on-event
import { ordinarySessions } from "../../../data/util/project-sessions.js";
import { ownerRows, ownerState } from "../../../data/owners-model.js";
import { ownersHealth, ownersSlice } from "../../../data/owners.js";
import { activeOwnerIdOf } from "../../Sidebar/sessions.js";

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

function sessionBrief(sess) {
  return sessionRowReason(sess);
}

export function drawerSessions(sessions, activeId) {
  const all = ordinarySessions(Object.values(sessions || {}));
  const active = all
    .filter((s) => s.state !== "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));
  const saved = all
    .filter((s) => s.state === "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));
  const toCard = (s) => {
    const dotState = sessionDisplayDotState(s);
    /* The phone gets the same second line as the desktop: why the session
       wants you. The "Needs you:" lead-in goes with it — it existed to bold a
       prefix in front of a conversation summary, and there is no summary on
       that line any more for it to introduce. */
    const reason = sessionBrief(s);
    return {
      id: s.id,
      title: sessionTitle(s),
      state: dotState,
      when: relAge(s.updated),
      // `brief`/`briefTone`, the names <Sidebar/> reads (sessions.js
      // toSpineRow). Named `last` here, the phone's rows never showed why a
      // session wanted you: the desktop said "Needs your answer", the phone
      // printed the path.
      brief: reason?.text || "",
      briefTone: reason?.tone || "",
      path: reason ? "" : (shortPath(s.cwd) || s.cwd || ""),
      unseen: !!s.unseen,
      active: s.id === activeId,
      saved: s.state === "saved",
      origin: s.origin || undefined,
      cwd: s.cwd || "",
      updated: s.updated || 0,
    };
  };
  const newResults = newResultSessions(active);
  const remainingActive = active.filter((s) => !newResults.includes(s));
  return {
    newResults: newResults.map(toCard),
    active: remainingActive.map(toCard),
    saved: saved.map(toCard),
    activeCount: active.length,
    savedCount: saved.length,
  };
}

export function drawerProjects(sessions) {
  const byCwd = {};
  for (const s of ordinarySessions(Object.values(sessions || {}))) {
    const cwd = s.cwd || "";
    if (!cwd) continue;
    const updated = s.updated || 0;
    if (!byCwd[cwd] || updated > byCwd[cwd].updated) byCwd[cwd] = { cwd, updated };
  }
  return Object.values(byCwd).sort((a, b) => b.updated - a.updated);
}

export function recentSavedSessions(sessions, limit = 3) {
  return ordinarySessions(Object.values(sessions || {}))
    .filter((s) => s.state === "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0))
    .slice(0, limit)
    .map((s) => ({
      id: s.id,
      title: sessionTitle(s),
      when: relAge(s.updated),
      path: shortPath(s.cwd) || s.cwd || "",
    }));
}

// recentOwners is the empty state's grid of faces: every owner, the most
// recently active conversation first, each with its own state — the same
// ownerState the sidebar row paints, so the face behaves here as it does
// there. Pass the joined rows (ownerRows): the owner's own conversation is
// hidden from the session list, and only the join knows its state and unseen.
export function recentOwners(owners = [], sessions = {}) {
  const at = (own) => sessions?.[own.session_id]?.updated || 0;
  return [...(owners || [])]
    .sort((a, b) => at(b) - at(a))
    .map((own) => ({
      id: own.id,
      name: own.name,
      session_id: own.session_id,
      avatar: own.avatar,
      codebase_key: own.codebase_key,
      state: ownerState(own),
    }));
}

// titleOwner is the owner whose OWN conversation is on screen, for the face in
// the header's title capsule; null for every other session (an ordinary
// session, or a child of an owner — that one wears the Owner chip instead).
export function titleOwner(session, owners = []) {
  if (!session || (session.kind || "") !== "owner") return null;
  const own = (owners || []).find((o) => o.session_id === session.id);
  if (!own) return null;
  return {
    id: own.id,
    name: own.name,
    avatar: own.avatar,
    codebase_key: own.codebase_key,
    state: ownerState(own),
  };
}

function titleOwnerSig(o) {
  return o ? [o.id, o.avatar?.shape || "", o.avatar?.color || "", o.codebase_key || "", o.state].join("\0") : "";
}

function recentOwnersSig(list) {
  return (list || []).map((o) => [o.id, o.name, o.session_id || "", o.avatar?.shape || "", o.avatar?.color || "", o.state || ""].join("\0")).join("\n");
}

function cardSig(row) {
  return [
    row.id, row.title, row.state, row.when, row.brief, row.briefTone || "",
    row.path, row.unseen ? 1 : 0, row.active ? 1 : 0, row.saved ? 1 : 0,
    row.origin || "",
  ].join("\0");
}

function listSig(list) {
  return (list || []).map(cardSig).join("\n");
}

function ownersSig(list) {
  return (list || []).map((o) => [
    o.id, o.name, o.session_state || "", o.unseen ? 1 : 0, o.ownReason || "",
    o.avatar?.shape || "", o.avatar?.color || "",
    (o.children || []).map((c) => c.id + c.state + (c.unseen ? 1 : 0)).join(","),
  ].join("\0")).join("\n");
}

function attentionSig(a) {
  return [a.urgent, a.unseen, a.error, a.permission, a.arrival].join("\0");
}

function mobileChromeEqual(a, b) {
  return a.activeId === b.activeId
    && a.drawerOpen === b.drawerOpen
    && a.drawerStep === b.drawerStep
    && a.groupByProject === b.groupByProject
    && a.sidebarMode === b.sidebarMode
    && a.activeOwnerId === b.activeOwnerId
    && a.ownersHealth?.status === b.ownersHealth?.status
    && a.ownersHealth?.error === b.ownersHealth?.error
    && ownersSig(a.owners) === ownersSig(b.owners)
    && a.soundEnabled === b.soundEnabled
    && a.showChip === b.showChip
    && a.title === b.title
    && a.activeCount === b.activeCount
    && a.savedCount === b.savedCount
    && a.drawerCollapsed === b.drawerCollapsed
    && a.collapsedSections === b.collapsedSections
    && attentionSig(a.attention) === attentionSig(b.attention)
    && a.inboxOpen === b.inboxOpen // wake-on-event
    && inboxSig(a.inbox) === inboxSig(b.inbox) // wake-on-event
    && inboxHealthSig(a.inboxHealth) === inboxHealthSig(b.inboxHealth) // wake-on-event
    && listSig(a.newResults) === listSig(b.newResults)
    && listSig(a.active) === listSig(b.active)
    && listSig(a.saved) === listSig(b.saved)
    && listSig(a.recentSaved) === listSig(b.recentSaved)
    && recentOwnersSig(a.recentOwners) === recentOwnersSig(b.recentOwners)
    && titleOwnerSig(a.titleOwner) === titleOwnerSig(b.titleOwner)
    && (a.projects || []).map((p) => `${p.cwd}\0${p.updated}`).join("\n")
      === (b.projects || []).map((p) => `${p.cwd}\0${p.updated}`).join("\n");
}

// Roster the chip and drawer paint. A stream token is not on a card, so the
// previous object is returned (Object.is) and MobileSessionChrome does not
// re-render while the conversation body does.
export function selectMobileChrome(state, forceMobile = false) {
  const activeId = forceMobile ? (state.activeSession || null) : focusedSessionId(state);
  const session = activeId ? state.sessions[activeId] : null;
  const lists = drawerSessions(state.sessions, activeId);
  const owners = ownerRows(ownersSlice(state).list, state.sessions);
  const next = {
    activeId,
    title: session ? sessionTitle(session) : "",
    showChip: !!(session && !session.viewingSubagent && !session.viewingBashJob),
    attention: aggregateAttention(state.sessions, activeId),
    drawerOpen: !!state.drawerOpen,
    drawerStep: state.drawerStep || "list",
    groupByProject: !!state.groupByProject,
    sidebarMode: state.sidebarMode || (state.groupByProject ? "project" : "recent"),
    owners,
    ownersHealth: ownersHealth(state),
    activeOwnerId: activeOwnerIdOf(state),
    drawerCollapsed: state.drawerCollapsed,
    // The Recent list's own accordion, beside the folder one.
    collapsedSections: state.collapsedSections,
    soundEnabled: !!state.soundEnabled,
    inbox: inboxCards(state.sessions, state.events), // wake-on-event
    inboxHealth: inboxHealth(state), // wake-on-event
    inboxOpen: !!state.inboxOpen, // wake-on-event
    projects: drawerProjects(state.sessions),
    recentSaved: recentSavedSessions(state.sessions),
    recentOwners: recentOwners(owners, state.sessions),
    titleOwner: titleOwner(session, owners),
    ...lists,
  };
  const prev = selectMobileChrome._prev;
  if (prev && mobileChromeEqual(prev, next)) return prev;
  selectMobileChrome._prev = next;
  return next;
}

export function __resetMobileChromeForTests() {
  selectMobileChrome._prev = null;
}
