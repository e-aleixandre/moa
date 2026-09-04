import { focusedSessionId } from "../../../data/selectors.js";
import { shortPath, sessionDisplayDotState, sessionTitle } from "../../../data/util/format.js";
import { sessionRowBrief } from "../../Spine/sessions.js";
import { aggregateAttention, newResultSessions } from "./attention-model.js";
import { inboxCards, inboxSig } from "../../../data/events.js"; // wake-on-event

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
  return sessionRowBrief(sess);
}

export function drawerSessions(sessions, activeId) {
  const all = Object.values(sessions || {});
  const active = all
    .filter((s) => s.state !== "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));
  const saved = all
    .filter((s) => s.state === "saved")
    .sort((a, b) => (b.updated || 0) - (a.updated || 0));
  const toCard = (s) => {
    const dotState = sessionDisplayDotState(s);
    const needs = dotState === "permission";
    return {
      id: s.id,
      title: sessionTitle(s),
      state: dotState,
      when: relAge(s.updated),
      last: sessionBrief(s),
      needsLabel: needs ? "Needs you:" : undefined,
      path: shortPath(s.cwd) || s.cwd || "",
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
  for (const s of Object.values(sessions || {})) {
    const cwd = s.cwd || "";
    if (!cwd) continue;
    const updated = s.updated || 0;
    if (!byCwd[cwd] || updated > byCwd[cwd].updated) byCwd[cwd] = { cwd, updated };
  }
  return Object.values(byCwd).sort((a, b) => b.updated - a.updated);
}

export function recentSavedSessions(sessions, limit = 3) {
  return Object.values(sessions || {})
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

function cardSig(row) {
  return [
    row.id, row.title, row.state, row.when, row.last, row.needsLabel || "",
    row.path, row.unseen ? 1 : 0, row.active ? 1 : 0, row.saved ? 1 : 0,
    row.origin || "",
  ].join("\0");
}

function listSig(list) {
  return (list || []).map(cardSig).join("\n");
}

function attentionSig(a) {
  return [a.urgent, a.unseen, a.error, a.permission, a.arrival].join("\0");
}

function mobileChromeEqual(a, b) {
  return a.activeId === b.activeId
    && a.drawerOpen === b.drawerOpen
    && a.drawerStep === b.drawerStep
    && a.groupByProject === b.groupByProject
    && a.soundEnabled === b.soundEnabled
    && a.showChip === b.showChip
    && a.previewOpen === b.previewOpen
    && a.title === b.title
    && a.activeCount === b.activeCount
    && a.savedCount === b.savedCount
    && a.drawerCollapsed === b.drawerCollapsed
    && attentionSig(a.attention) === attentionSig(b.attention)
    && a.inboxOpen === b.inboxOpen // wake-on-event
    && inboxSig(a.inbox) === inboxSig(b.inbox) // wake-on-event
    && listSig(a.newResults) === listSig(b.newResults)
    && listSig(a.active) === listSig(b.active)
    && listSig(a.saved) === listSig(b.saved)
    && listSig(a.recentSaved) === listSig(b.recentSaved)
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
  const next = {
    activeId,
    title: session ? sessionTitle(session) : "",
    showChip: !!(session && !session.viewingSubagent && !session.viewingBashJob),
    previewOpen: !!session?.previewOpen,
    attention: aggregateAttention(state.sessions, activeId),
    drawerOpen: !!state.drawerOpen,
    drawerStep: state.drawerStep || "list",
    groupByProject: !!state.groupByProject,
    drawerCollapsed: state.drawerCollapsed,
    soundEnabled: !!state.soundEnabled,
    inbox: inboxCards(state.sessions, state.events), // wake-on-event
    inboxOpen: !!state.inboxOpen, // wake-on-event
    projects: drawerProjects(state.sessions),
    recentSaved: recentSavedSessions(state.sessions),
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
